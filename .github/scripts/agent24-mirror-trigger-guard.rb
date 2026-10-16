# frozen_string_literal: true
# 推镜像前的守卫。用同步 App 令牌推送会产生 push 事件，push 事件运行的是被推送提交里的
# 工作流定义（上游代码，未经评审）。任一工作流会被推送到镜像分支激活（push/create），
# 或内容无法识别，就失败停止，交给人处理。
#
# 同步 PR 由内置 GITHUB_TOKEN 创建，不会自动运行上游工作流，不在这里检查；
# 它的评审步骤见 docs/agent24/FORK-MAINTENANCE.md。
#
# 用法：ruby agent24-mirror-trigger-guard.rb <commit> <branch> [<commit> <branch> ...]
# 只读 git 对象，不检出、不执行上游代码。
require "yaml"
require "open3"

# 推送只可能触发这些用被推送提交里的定义的事件；schedule、workflow_run 等用默认分支（已评审）的定义。
PUSH_EVENTS = %w[push create].freeze
# GitHub 方括号只允许字母数字与 a-z / A-Z / 0-9 范围。
BRACKET_BODY = /\A(?:[A-Za-z0-9](?:-[A-Za-z0-9])?)+\z/

class Unknown < StandardError; end

def git(*args)
  out, status = Open3.capture2("git", *args)
  raise "git #{args.join(' ')} failed" unless status.success?
  # 不依赖运行环境的 locale：git 输出按 UTF-8 处理，编码非法的内容按有风险处理
  out.force_encoding(Encoding::UTF_8)
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
    inc = Array(inc).map(&:to_s)
    return true if inc.empty? # 空列表的语义文档未说明，按会触发处理
    # 只看正向模式；! 否定一律忽略（只会多拦）
    return inc.reject { |p| p.start_with?("!") }.any? { |p| may_match?(p, branch) }
  end
  exc = Array(exc).map(&:to_s)
  # 排除列表里的 ! 会按顺序把分支重新包含回来；出现就按会触发处理
  return true if exc.any? { |p| p.start_with?("!") }
  exc.none? { |p| surely_matches?(p, branch) }
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
    unless name&.valid_encoding?
      problems << "#{commit[0, 10]} .github/workflows 下有无法识别的文件名，按有风险处理"
      next
    end
    next unless name.downcase.end_with?(".yml", ".yaml")
    path = ".github/workflows/#{name}"
    tag = "#{commit[0, 10]} #{path}"
    unless meta.split(" ")[1] == "blob"
      problems << "#{tag}: 不是普通文件，按有风险处理"
      next
    end
    begin
      raw = git("cat-file", "blob", meta.split(" ")[2])
      raise Unknown, "内容不是合法 UTF-8" unless raw.valid_encoding?
      # Psych 处理合并键 << 的优先级与规范相反，可能把分支过滤解析错；块式与 flow 映射里
      # 出现就按无法识别处理（宁可误拦 run 脚本里恰好写了 <<: 的文件）
      raise Unknown, "含 YAML 合并键 <<" if raw.match?(/<<\s*:/)
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
    rescue Psych::Exception, Unknown, ArgumentError, EncodingError => e
      problems << "#{tag}: 无法确认触发条件（#{e.class}: #{e.message}），按有风险处理"
    end
  end
end

if problems.empty?
  puts "mirror trigger guard: ok"
else
  problems.each { |p| puts "::error::#{p}" }
  puts "上游工作流会被镜像推送以未评审的方式运行，已停止。处理办法见 docs/agent24/FORK-MAINTENANCE.md「同步凭据」。"
  exit 1
end
