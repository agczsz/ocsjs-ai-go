# Go 版本 AI 题库服务运行指南

本文件夹包含使用 Go 语言重构的 `ocsjs-ai-answer-service` 服务源码及可执行文件。

## 编译方法

如果您修改了源码，可以运行文件夹内的 `build.bat` 进行重新编译，或者手动在当前目录下执行命令：
```cmd
go build -ldflags "-s -w" -o ocs_ai_answer_service.exe
```

## 运行方式与参数说明

您可以在命令行中直接指定 API 链接、密钥和模型等参数。以下是调用示例：

```cmd
.\ocs_ai_answer_service.exe -api-base "【用户自行填写】" -api-key "【用户自行填写】" -model evomap-deepseek-v4-flash -port 5000
```

### 可用参数列表

| 参数名 | 说明 | 默认值 |
|---|---|---|
| `-api-base` | OpenAI 兼容接口 base URL | `https://api.openai.com/v1` |
| `-api-key` | OpenAI 兼容接口 API 密钥 | 无（启动必填） |
| `-model` | 调用的模型名称 | `gpt-3.5-turbo` |
| `-port` | 服务端口 | `5000` |
| `-host` | 服务绑定 Host | `0.0.0.0` |
| `-proxy` | 网络代理地址 (如 `http://127.0.0.1:7890`) | 无 |
| `-token` | 安全访问令牌 | 无 |
| `-log-level` | 日志等级 (DEBUG/INFO/WARNING/ERROR) | `INFO` |
| `-multi-mode` | 多路模型/策略模式 (`standard` / `confidence` / `fallback`) | `standard` |
| `-threshold` | 置信度评分过滤阈值 (0 到 1 之间) | `0.7` |
| `-exa-key` | Exa AI 联网搜索 API 密钥 | 无 |
| `-model1` | Fallback 模式第 1 顺位模型名称 | 无 |
| `-api-base1` | Fallback 模式第 1 顺位 API Base URL | 无 (默认使用 `-api-base`) |
| `-api-key1` | Fallback 模式第 1 顺位 API Key | 无 (默认使用 `-api-key`) |
| `-model2` | Fallback 模式第 2 顺位模型名称 | 无 |
| `-api-base2` | Fallback 模式第 2 顺位 API Base URL | 无 (默认使用 `-api-base`) |
| `-api-key2` | Fallback 模式第 2 顺位 API Key | 无 (默认使用 `-api-key`) |
| `-model3` | Fallback 模式第 3 顺位模型名称 | 无 |
| `-api-base3` | Fallback 模式第 3 顺位 API Base URL | 无 (默认使用 `-api-base`) |
| `-api-key3` | Fallback 模式第 3 顺位 API Key | 无 (默认使用 `-api-key`) |

## 特色机制

### 1. SQLite3 本地持久化缓存 (`cache.db`)
服务运行后，题库缓存将被持久化为本地 SQLite3 数据库文件 `cache.db`：
- **无 TTL 限制**：缓存永久有效。
- **一模一样题目直接命中**：问题内容、选项及题型完全一致时，将从本地直接返回。
- **选项乱序智能映射**：服务支持自动映射打乱了选项顺序（例如 A 变成了 B）的单选/多选题。即使顺序改变，也会判定缓存命中，并根据当前的选项排列动态计算出最新的正确字母选项返回，免去重复请求 API 的消耗。

### 2. 空答案自动重试与跳过
- 若首次获取 of AI 答案为空（或接口报错），将自动**重新请求一次 API**。
- 若第二次尝试获取的答案依然为空，将直接**跳过该问题**，并向客户端返回包含跳过提示的响应 (`code: 0, msg: "获取答案为空，已跳过"`).

### 3. 多策略执行模式

#### A. 置信度评估与联网检索模式 (`-multi-mode confidence`)
1. 获得主模型初始答案后，对其置信度（0-1 之间）进行评估。
2. 若得分 $\ge$ 阈值，则直接返回；否则，判定为低置信度。
3. 若配置了 `-exa-key`，将通过 Exa AI 自动提取 3 条互联网相关上下文，带上信息重新向主模型提问，获取第二版最优答案。若无 Key，则带上首次答案由模型在低置信提示下重新分析获得新答案。

#### B. 多模型顺序备选模式 (`-multi-mode fallback`)
- 按照配置的顺序逐个请求 `-model1` $\rightarrow$ `-model2` $\rightarrow$ `-model3`。
- 每个备选模型都可以通过 `-api-baseX` 和 `-api-keyX` 设置专属的 API 接口与密钥，未配置时自动回退使用默认的 API 接口与密钥。
- 请求直至其中任意一个返回有效的答题选项结果，则立即截断并返回，最大化服务可用性。

