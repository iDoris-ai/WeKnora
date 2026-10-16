# frozen_string_literal: true
# 推镜像前的守卫：用同步 App 令牌推送会产生 push 事件，push 事件运行的是
# 被推送提交里的工作流定义（上游代码，未经评审）。若其中任何工作流会被
# 推送到镜像分支触发，就失败停止，交给人处理。
#
# 用法：ruby agent24-mirror-trigger-guard.rb <commit> <branch> [<commit> <branch> ...]
# 只读 git 对象，不检出、不执行上游代码。
require "yaml"
require "open3"

# 推送镜像只可能触发这些用被推送提交里的定义的事件；其余事件用默认分支（已评审）的定义。
RISKY_EVENTS = %w[push create].freeze

def git(*args)
  out, status = Open3.capture2("git", *args)
  raise "git #{args.join(' ')} failed" unless status.success?
  out
end

# GitHub 分支过滤 glob：** 任意字符，* 不跨 /，? 单字符，其余按字面。
def glob_match?(pattern, name)
  re = Regexp.escape(pattern).gsub("\\*\\*", "\u0000").gsub("\\*", "[^/]*").gsub("\u0000", ".*").gsub("\\?", ".")
  name.match?(/\A#{re}\z/)
end

def branch_triggers?(cfg, branch)
  return true unless cfg.is_a?(Hash) # 无过滤：任何分支推送都触发
  inc = cfg["branches"]
  exc = cfg["branches-ignore"]
  if inc.nil? && exc.nil?
    # 只写了 tags/tags-ignore 时分支推送不触发；只写 paths 时仍触发（保守：paths 不作为豁免）
    return !(cfg.key?("tags") || cfg.key?("tags-ignore"))
  end
  if inc
    # 保守：只看正向模式，忽略 ! 否定
    return Array(inc).map(&:to_s).reject { |p| p.start_with?("!") }.any? { |p| glob_match?(p, branch) }
  end
  !Array(exc).map(&:to_s).any? { |p| glob_match?(p, branch) }
end

problems = []
ARGV.each_slice(2) do |commit, branch|
  raise "need <commit> <branch> pairs" if branch.nil?
  files = git("ls-tree", "--name-only", "#{commit}:.github/workflows").split("\n")
  files.select { |f| f.end_with?(".yml", ".yaml") }.each do |f|
    path = ".github/workflows/#{f}"
    begin
      doc = YAML.safe_load(git("show", "#{commit}:#{path}"), aliases: true) || {}
    rescue Psych::Exception => e
      problems << "#{commit[0, 10]} #{path}: 无法解析（#{e.class}），按有风险处理"
      next
    end
    on = doc.key?("on") ? doc["on"] : doc[true] # YAML 1.1 把 on 解析成 true
    events =
      case on
      when String then { on => nil }
      when Array then on.to_h { |e| [e.to_s, nil] }
      when Hash then on.transform_keys(&:to_s)
      else {}
      end
    RISKY_EVENTS.each do |ev|
      next unless events.key?(ev)
      if ev == "create" || branch_triggers?(events[ev], branch)
        problems << "#{commit[0, 10]} #{path}: `#{ev}` 触发会被推送到 #{branch} 激活"
      end
    end
  end
end

if problems.empty?
  puts "mirror trigger guard: ok"
else
  problems.each { |p| puts "::error::#{p}" }
  puts "上游工作流会被镜像推送触发，已停止。处理办法见 docs/agent24/FORK-MAINTENANCE.md「同步凭据」。"
  exit 1
end
