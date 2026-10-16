# frozen_string_literal: true
# 推镜像前的守卫。用同步 App 令牌推送会产生 push 事件，开同步 PR 或推进其 head
# 会产生 pull_request 事件；这两类事件运行的都是上游提交里的工作流定义（未经评审）。
#   - 任一工作流会被推送到镜像分支激活（push/create）：失败停止。
#   - PR head 分支（upstream-release）的目标提交里，pull_request 类工作流有任一 job
#     可能拿到写权限的 GITHUB_TOKEN（未声明 permissions、或声明了 write），或在表达式里
#     引用了白名单以外的 secret、动态访问 secrets、使用 secrets: inherit：失败停止。
#   - 无法解析或无法识别的内容一律按有风险处理。
#
# 用法：ruby agent24-mirror-trigger-guard.rb <commit> <branch> [<commit> <branch> ...]
# 只读 git 对象，不检出、不执行上游代码。
require "yaml"
require "open3"

PR_HEAD_BRANCH = "upstream-release"
# 这些事件用被推送提交里的定义；其余事件（schedule、workflow_run、pull_request_target 等）用默认分支已评审的定义。
PUSH_EVENTS = %w[push create].freeze
PR_EVENTS = %w[pull_request pull_request_review pull_request_review_comment merge_group].freeze
# 同步 PR 运行的工作流可以引用的 secret 名（大小写不敏感）。GITHUB_TOKEN 的权限另由 permissions 检查约束；
# 其余名字在本仓库、组织与各 Environment 里都不存在（2026-10-16 核实：组织对本仓库可见的只有 CLA_TOKEN），
# 引用时取到空值。给本仓库新增可见 secret 前，先确认它不在这个列表里。
PR_SECRET_ALLOWLIST = %w[GITHUB_TOKEN WEKNORA_E2E_HOST WEKNORA_E2E_TOKEN NPM_TOKEN].freeze
# GitHub 方括号只允许字母数字与 a-z / A-Z / 0-9 范围。
BRACKET_BODY = /\A(?:[A-Za-z0-9](?:-[A-Za-z0-9])?)+\z/

class Unknown < StandardError; end

def git(*args)
  out, status = Open3.capture2("git", *args)
  raise "git #{args.join(' ')} failed" unless status.success?
  out
end

# GitHub 过滤模式（workflow-syntax 的 Filter pattern cheat sheet）转正则：
# ** 任意字符；* 不跨 /；? 前一字符零或一次；+ 前一字符一或多次；[...] 字符集；其余按字面。
def glob_to_regex(pattern)
  out = +""
  quantified = false # 上一个原子是否已带量词；叠加量词（*+、?+、++ 等）在 Ruby 里另有含义，按无法识别处理
  i = 0
  while i < pattern.length
    c = pattern[i]
    if c == "*" && pattern[i + 1] == "*"
      out << ".*"; i += 2; quantified = true
    elsif c == "*"
      out << "[^/]*"; i += 1; quantified = true
    elsif c == "?" || c == "+"
      raise Unknown, pattern if out.empty? || quantified
      out << c; i += 1; quantified = true
    elsif c == "["
      j = pattern.index("]", i + 1) or raise Unknown, pattern
      body = pattern[(i + 1)...j]
      raise Unknown, pattern unless body.match?(BRACKET_BODY)
      out << "[#{body}]"; i = j + 1; quantified = false
    elsif c == "\\"
      raise Unknown, pattern
    else
      out << Regexp.escape(c); i += 1; quantified = false
    end
  end
  Regexp.new("\\A#{out}\\z")
rescue RegexpError
  raise Unknown, pattern
end

# 返回 true 表示该模式可能匹配 branch；无法识别的模式按"匹配"处理。
def may_match?(pattern, branch)
  branch.match?(glob_to_regex(pattern))
rescue Unknown
  true
end

# 返回 true 表示该模式一定匹配 branch（用于 branches-ignore 排除）；无法识别的按"不匹配"处理。
def surely_matches?(pattern, branch)
  branch.match?(glob_to_regex(pattern))
rescue Unknown
  false
end

def branch_push_triggers?(cfg, branch)
  return true if cfg.nil?
  raise Unknown, "push 配置不是映射" unless cfg.is_a?(Hash)
  inc = cfg["branches"]
  exc = cfg["branches-ignore"]
  if inc.nil? && exc.nil?
    # 只写 tags/tags-ignore 时分支推送不触发；只写 paths 时仍触发（paths 不作为豁免）
    return !(cfg.key?("tags") || cfg.key?("tags-ignore"))
  end
  if inc
    # 只看正向模式；! 否定一律忽略（只会多拦）
    return Array(inc).map(&:to_s).reject { |p| p.start_with?("!") }.any? { |p| may_match?(p, branch) }
  end
  exc = Array(exc).map(&:to_s)
  # 排除列表里的 ! 会按顺序把分支重新包含回来；出现就按会触发处理
  return true if exc.any? { |p| p.start_with?("!") }
  exc.none? { |p| surely_matches?(p, branch) }
end

# id-token: write 只允许向 GitHub 申请 OIDC 身份令牌，不授予仓库写权限；本仓库没有任何外部服务信任它。
NON_REPO_WRITE_SCOPES = %w[id-token].freeze

def perms_may_write?(perms)
  return true if perms.nil? # 未声明：取仓库默认值，按可能为 write 处理
  return perms.to_s != "read-all" unless perms.is_a?(Hash)
  perms.any? { |k, v| v.to_s == "write" && !NON_REPO_WRITE_SCOPES.include?(k.to_s) }
end

def pr_job_may_write?(doc)
  jobs = doc["jobs"]
  raise Unknown, "jobs 不是映射" unless jobs.is_a?(Hash)
  top = doc["permissions"]
  jobs.values.any? do |job|
    raise Unknown, "job 不是映射" unless job.is_a?(Hash)
    perms_may_write?(job.key?("permissions") ? job["permissions"] : top)
  end
end

# 同步 PR 运行的工作流在表达式里对 secrets 的引用。只看 ${{ }} 表达式（secrets 上下文只能在表达式里用），
# 外加 secrets: inherit。steps.secrets.outputs 这类步骤 id 不算。
def pr_secret_problems(raw)
  out = []
  out << "`secrets: inherit` 会把全部 secret 交给可复用工作流" if raw.match?(/^\s*secrets\s*:\s*inherit\b/)
  raw.scan(/\$\{\{(.*?)\}\}/m).flatten.each do |expr|
    expr.scan(/(?<![\w.\-])secrets\b(?:\s*\.\s*([A-Za-z_][A-Za-z0-9_\-]*))?/i) do |m|
      name = m[0]
      if name.nil?
        out << "表达式动态访问 secrets：#{expr.strip[0, 60]}"
      elsif PR_SECRET_ALLOWLIST.none? { |a| a.casecmp?(name) }
        out << "引用了 secret #{name}"
      end
    end
  end
  out.uniq
end

problems = []
raise "need <commit> <branch> pairs" if ARGV.empty? || ARGV.length.odd?
ARGV.each_slice(2) do |commit, branch|
  git("cat-file", "-e", "#{commit}^{commit}") # 目标提交必须存在
  # 没有 .github/workflows 目录：不会触发任何工作流
  _, wf_status = Open3.capture2e("git", "cat-file", "-e", "#{commit}:.github/workflows")
  next unless wf_status.success?
  entries = git("ls-tree", "-z", "#{commit}:.github/workflows").split("\0")
  entries.each do |entry|
    meta, name = entry.split("\t", 2)
    next unless name.end_with?(".yml", ".yaml")
    path = ".github/workflows/#{name}"
    tag = "#{commit[0, 10]} #{path}"
    unless meta.split(" ")[1] == "blob"
      problems << "#{tag}: 不是普通文件，按有风险处理"
      next
    end
    begin
      raw = git("cat-file", "blob", meta.split(" ")[2])
      # Psych 处理合并键 << 的优先级与规范相反，可能把分支过滤解析错；出现就按无法识别处理
      raise Unknown, "含 YAML 合并键 <<" if raw.match?(/^\s*(?:-\s*)?<<\s*:/)
      doc = YAML.safe_load(raw, aliases: true)
      raise Unknown, "顶层不是映射" unless doc.is_a?(Hash)
      on = doc.key?("on") ? doc["on"] : doc[true] # YAML 1.1 把 on 解析成 true
      events =
        case on
        when String then { on => nil }
        when Array then on.to_h { |e| [e.to_s, nil] }
        when Hash then on.transform_keys(&:to_s)
        else raise Unknown, "无法识别的 on"
        end
      PUSH_EVENTS.each do |ev|
        next unless events.key?(ev)
        if ev == "create" || branch_push_triggers?(events[ev], branch)
          problems << "#{tag}: `#{ev}` 触发会被推送到 #{branch} 激活"
        end
      end
      if branch == PR_HEAD_BRANCH && (events.keys & PR_EVENTS).any?
        problems << "#{tag}: 同步 PR 会运行它，且有 job 可能拿到写权限的 GITHUB_TOKEN" if pr_job_may_write?(doc)
        pr_secret_problems(raw).each { |m| problems << "#{tag}: 同步 PR 会运行它，#{m}" }
      end
    rescue Psych::Exception, Unknown => e
      problems << "#{tag}: 无法确认触发条件（#{e.class}: #{e.message}），按有风险处理"
    end
  end
end

if problems.empty?
  puts "mirror trigger guard: ok"
else
  problems.each { |p| puts "::error::#{p}" }
  puts "上游工作流会被镜像推送或同步 PR 以未评审的方式运行，已停止。处理办法见 docs/agent24/FORK-MAINTENANCE.md「同步凭据」。"
  exit 1
end
