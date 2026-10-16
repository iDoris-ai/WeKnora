# frozen_string_literal: true
# 推镜像前的守卫。用同步 App 令牌推送会产生 push 事件，push 事件运行的是被推送提交里的
# 工作流定义（上游代码，未经评审）。任一工作流会被推送到镜像分支激活（push/create），
# 或内容无法识别，就失败停止，交给人处理。
#
# 同步工作流不开 PR（只开或更新同步 issue），PR 类事件不在这里检查；
# 评审人开同步 PR 前的步骤见 docs/agent24/FORK-MAINTENANCE.md。
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
# 平台语义有疑问的写法按 wide 取舍：wide 为 true 时取较宽的解释（用于"可能匹配"，宁可多拦），
# 为 false 时取较窄的解释（用于"一定匹配"的排除）。cheat sheet 里 '**/README.md' 匹配 README.md，
# 即 **/ 可以匹配零层目录；其它与 ** 相邻的 /（结尾的 /**、***/、/**/** 等）文档没写，
# wide 下一律当作可省略。大小写是否敏感文档也没写，wide 下不区分大小写。
def glob_to_regex(pattern, wide:)
  out = +""
  quantified = false # 上一个原子是否已带量词；叠加量词（*+、?+、++ 等）在 Ruby 里另有含义，按无法识别处理
  i = 0
  while i < pattern.length
    c = pattern[i]
    if wide && c == "/" && ((i >= 2 && pattern[i - 2, 2] == "**") || pattern[i + 1, 2] == "**")
      out << "/?"; i += 1; quantified = true
    elsif c == "*" && pattern[i + 1] == "*"
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
  Regexp.new("\\A#{out}\\z", wide ? Regexp::IGNORECASE : 0)
rescue RegexpError
  raise Unknown, pattern
end

# 返回 true 表示该模式可能匹配 branch；无法识别的模式按"匹配"处理。
# GitHub 是否拿 refs/heads/<name> 全名去匹配，文档没写；按宽解释，短名和全名任一匹配都算。
def may_match?(pattern, branch)
  re = glob_to_regex(pattern, wide: true)
  [branch, "refs/heads/#{branch}"].any? { |b| b.match?(re) }
rescue Unknown
  true
end

# 返回 true 表示该模式一定匹配 branch（用于 branches-ignore 排除）；无法识别的按"不匹配"处理。
def surely_matches?(pattern, branch)
  branch.match?(glob_to_regex(pattern, wide: false))
rescue Unknown
  false
end

# YAML 里 Psych 与 GitHub 可能读出不同结果的结构，出现就按无法识别处理：多个文档（safe_load 只读第一个）、
# 同一映射里的重复键（safe_load 取最后一个；"on" 与裸 on 原文相同，也算重复）、
# 非普通标量的键（复杂键、别名、带标签）、带标签的任何节点（如 !!binary 的值，Psych 会解码，
# GitHub 的解析器直接报错），以及合并键 <<（Psych 会合并，GitHub 不支持；
# 按反转义后的键值判断，"<<"、"\x3c\x3c" 等任何写法都算）。
KEY_SCANNER = Psych::ScalarScanner.new(Psych::ClassLoader::Restricted.new([], []))
def reject_ambiguous_yaml!(raw)
  docs = Psych.parse_stream(raw).children
  raise Unknown, "含多个 YAML 文档" if docs.length > 1
  docs.each do |doc|
    doc.each do |node|
      raise Unknown, "含带标签的节点" if node.respond_to?(:tag) && node.tag && !node.is_a?(Psych::Nodes::Document)
      next unless node.is_a?(Psych::Nodes::Mapping)
      raw_keys = []; resolved = []
      node.children.each_slice(2) do |key, _|
        raise Unknown, "映射键不是普通标量" unless key.is_a?(Psych::Nodes::Scalar) && key.tag.nil?
        raise Unknown, "含 YAML 合并键 <<" if key.value == "<<"
        raw_keys << key.value
        resolved << (key.quoted ? key.value : KEY_SCANNER.tokenize(key.value))
      end
      raise Unknown, "映射里有重复键" if raw_keys.uniq.length != raw_keys.length || resolved.uniq.length != resolved.length
    end
  end
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
  # .github 或 .github/workflows 存在但不是目录（例如符号链接）：无法确认，按有风险处理
  bad_dir = %w[.github .github/workflows].find do |d|
    type, st = Open3.capture2e("git", "cat-file", "-t", "#{commit}:#{d}")
    st.success? && type.strip != "tree"
  end
  if bad_dir
    problems << "#{commit[0, 10]} #{bad_dir} 不是普通目录（可能是符号链接），按有风险处理"
    next
  end
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
    mode, type = meta.split(" ")
    unless type == "blob" && mode != "120000"
      problems << "#{tag}: 不是普通文件（目录、子模块或符号链接），按有风险处理"
      next
    end
    begin
      raw = git("cat-file", "blob", meta.split(" ")[2])
      raise Unknown, "内容不是合法 UTF-8" unless raw.valid_encoding?
      reject_ambiguous_yaml!(raw)
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
