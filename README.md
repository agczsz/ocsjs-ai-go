# ocsjs-ai-go
基于Go和OpenAI API的新一代智能题库服务，专为OCS (Online Course Script)设计，可以通过AI自动回答题目。此服务实现了与OCS AnswererWrapper兼容的API接口，方便用户将AI能力整合到OCS题库搜索中。

## 编译方法

特点
1.允许缓存题目
2.允许socks5代理
3.轻量，直接二进制运行

## 编译方法

运行文件夹内的 `build.bat` 进行重新编译。
```cmd
go build -ldflags "-s -w" -o ocs_ai_answer_service.exe
```

## 运行方式与参数说明

您可以在命令行中直接指定 API 链接、密钥和模型等参数。以下是调用示例：

```cmd
.\ocs_ai_answer_service.exe -api-base "【用户自行填写】" -api-key "【用户自行填写】" -model 【用户自行填写】 -port 5000
.\ocs_ai_answer_service.exe -api-base "https://xxx.com/v1" -api-key "sk-xxx" -model deepseek-v4-flash -port 5000
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

## 缓存机制

服务运行后，题库缓存将被持久化为本地的 `cache.json` 文件。
当遇到**一模一样**的题目（包括问题内容、选项及题型完全一致时），将直接从 `cache.json` 中读取答案并返回，而不会重复请求 AI 接口，实现高效应答与额度节省。
