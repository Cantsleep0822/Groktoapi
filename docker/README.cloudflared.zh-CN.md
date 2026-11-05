# Render 上的 grok2api 通过 Cloudflare Tunnel 连接 Cloady 上的 Resin

本教程适用于这次适配后的两个源码包。Resin 继续部署在 Cloady，grok2api 继续部署在 Render。原有 Resin 管理域名可以保留；另外准备一个专门供隧道使用的子域名，本文用 `resin-tcp.example.com` 演示。这个域名需要换成你自己的。

## 1. 为什么原来的地址连接超时

如果错误是 `dial tcp 104.21...:2261 i/o timeout`，连接到的是 Cloudflare 边缘服务器的 `2261`，而不是 Resin。普通橙云代理不支持把这个端口作为 SOCKS5 转发。改成 `:443` 也不能直接把 SOCKS5 变成 HTTPS。

日志中的 `address_family=ipv6` 和 `https://[2606:4700:4700::1111]/cdn-cgi/trace` 描述的是探测目标。这条日志本身不能证明 Render 到代理的连接因为缺少 IPv6 而失败。

这次使用下面的连接方式：

```text
grok2api
  → Render 容器内 127.0.0.1:2261（SOCKS5）
  → Render 容器内 cloudflared
  → Cloudflare HTTPS/WebSocket 443 + Access 服务认证
  → Cloady 容器内 cloudflared
  → Cloady 容器内 Resin 127.0.0.1:2260
  → Resin 选中的上游节点
  → Grok
```

Render 不需要直接连接 Cloady 的公网 IPv6。Cloady 的 cloudflared 主动出站连接 Cloudflare；此适配使用 HTTP/2，需要允许出站 TCP `7844`。Render 客户端使用出站 HTTPS `443`。这两个端口不要混淆。

## 2. 先认清三种密码

| 名称 | 从哪里获得 | 填到哪里 |
| --- | --- | --- |
| Tunnel Token | 创建 Cloudflare Tunnel 后，安装命令中 `--token` 后面的内容 | Cloady 环境变量 `RESIN_CF_TUNNEL_TOKEN` |
| Access Client ID / Client Secret | Cloudflare Access 创建 Service Token 后获得 | Render 的 `TUNNEL_SERVICE_TOKEN_ID` / `TUNNEL_SERVICE_TOKEN_SECRET` |
| Resin 代理密码 | 你原来设置的 `RESIN_PROXY_TOKEN` | grok2api 的 SOCKS5 代理 URL |

它们不能互换。Tunnel Token 不是 Cloudflare Global API Key，也不是你自己创建的通用 API Token。Resin 管理密码不需要提供给 Render。

不要把真实密码写进源码、ZIP 或 GitHub 仓库。下面示例中的域名和密码都是占位内容。

## 3. 在 Cloudflare 创建服务器端 Tunnel

1. 打开 Cloudflare Zero Trust，找到 Cloudflare Tunnel 的管理页面，通常在 **Networks → Connectors / Tunnels**。
2. 创建一个 **cloudflared** 类型的 Tunnel，名称可填 `resin-cloady`。
3. 在连接器安装页面，找到类似 `cloudflared ... run --token 一大串内容` 的命令。
4. 只复制 `--token` 后面的那串 Token，保存为服务器端 Tunnel Token。不必在本机执行整条安装命令，也不需要把 Token 发给别人。
5. 为此 Tunnel 添加 Published application / Public hostname：
   - 子域名：`resin-tcp`。
   - 域名：你的真实域名。
   - 服务类型：**TCP**。
   - 服务地址：`127.0.0.1:2260`。
   - 完整含义应为：`tcp://127.0.0.1:2260`。
6. 保存路由。

这里使用 Resin 原本就具备 SOCKS5 能力的 `2260`，不依赖你手动新建 `2261` 接入点。不要将 Tunnel 的服务类型选成 HTTP 或 HTTPS；不要将目标填成 Cloady 公网地址。

这个专用子域名应由 Tunnel 指向 `<Tunnel-ID>.cfargotunnel.com`。Cloudflare 通常会自动创建对应 DNS 记录。如果同名子域名已有指向 Cloady 的 CNAME，会产生冲突；换一个未使用的子域名即可。原来的 Resin 网页管理域名可以继续保留。

## 4. 创建无人值守的 Access 认证

1. 在 Zero Trust 的 Access 中找到 **Service credentials / Service tokens**。
2. 新建一个服务令牌，例如 `render-grok2api`，选择合适的有效期。
3. 保存它的 **Client ID** 和 **Client Secret**。Secret 通常只在创建时完整显示一次。
4. 在 **Access → Applications** 新建 **Self-hosted** 应用。
5. 受保护的域名填写刚创建的 `resin-tcp.你的域名`，不要附加端口或路径。
6. 给这个应用添加策略：
   - Action：**Service Auth**。
   - Include 条件：**Service Token**。
   - 选中刚才的 `render-grok2api` 服务令牌。
7. 保存应用和策略。

本方案要求服务令牌认证。只添加邮箱登录的 Allow 策略会让无人值守服务器无法完成认证。服务令牌过期或被撤销后，需要在 Render 更新对应值。

## 5. 更新 Cloady 上的 Resin

使用新包 `resin-cloady-cloudflared.zip` 的源码进行构建。在 Cloady 应用环境变量中新增：

```text
RESIN_CF_TUNNEL_TOKEN=服务器端TunnelToken的实际值
```

原来的 `RESIN_ADMIN_TOKEN`、`RESIN_PROXY_TOKEN`、数据卷和低资源配置继续使用。新包仍是一个 Resin 容器、一个数据卷；cloudflared 在容器内部运行，不增加额外 Compose 服务。两进程合用原有容器资源上限。

重新构建并部署后：

- Cloudflare Tunnel 页面应显示连接器在线。
- Cloady 日志应先显示 Tunnel 注册成功，再显示 `cloudflared supervisor: local tunnel ready; starting application`。
- Resin 管理页面仍应能正常打开。
- Resin 中应有健康的代理节点。

如果 Tunnel Token 未设置，Resin 会按原方式启动，隧道不会启用。如果设置了但连接器无法注册，启动检查会报错，不会伪装成隧道已就绪。

已有上传应用更新前先备份，确认平台实际使用了新 Dockerfile 和新增环境变量。不要删除原数据卷。此方案无需为隧道新开公网端口；旧的 `2261/tcp` 可以保留，但 Render 不再连接它。

## 6. 更新 Render 上的 grok2api

使用新包 `grok2api-render-cloudflared.zip` 中的源码更新你自己的部署仓库，保持仓库根目录包含 Dockerfile、backend、frontend、docker。Render 必须从这份修改后的 Dockerfile **重新构建镜像**；继续使用上游的预构建镜像不会包含这次适配。

在原有 Render Web Service 的 Environment 中添加：

| Key | Value |
| --- | --- |
| `GROK2API_CF_ACCESS_ENABLED` | `true` |
| `GROK2API_CF_ACCESS_HOSTNAME` | `resin-tcp.你的真实域名` |
| `TUNNEL_SERVICE_TOKEN_ID` | Access Client ID 的实际值 |
| `TUNNEL_SERVICE_TOKEN_SECRET` | Access Client Secret 的实际值 |

`GROK2API_CF_ACCESS_HOSTNAME` 只写主机名，不要带 `https://`、`:2261` 或任何路径。

默认的本机代理端口是 `2261`，通常不用另配。确实有冲突时，可增加 `GROK2API_CF_ACCESS_PORT`，同时修改下一节的代理 URL。它只监听 `127.0.0.1`，不需要将它作为 Render 的对外服务端口。

保留原来可用的 grok2api 配置和数据存储方式。新增启动兼容会识别 Render 的 `/etc/secrets/config.yaml`，也继续支持 `GROK2API_CONFIG_SOURCE` 指定的配置文件。默认启动命令会遵循 Render 提供的 `PORT`。不要用包中的配置示例覆盖已有 JWT 密钥、凭据加密密钥、账号或数据库。

Render 的 Docker Command 保持默认；如果原来有覆盖启动命令，要确保仍经过镜像中的 entrypoint，而不是绕过它启动程序。

重新部署后，日志中应出现 `cloudflared` 的本地 WebSocket listener，以及 `cloudflared supervisor: local tunnel ready; starting application`。这一行只代表本地客户端端口已打开；完整认证和远端转发要通过下一节的实际代理探测验证。

## 7. 修改 grok2api 的 Resin 节点

在 grok2api 的出口代理管理中编辑原来的 Resin 节点，将代理 URL 改成：

```text
socks5h://Default.{account}:RESIN_PROXY_TOKEN的实际值@127.0.0.1:2261
```

- `Default` 是 Resin 平台名，大小写按实际平台填写。
- `{account}` 原样保留，由 grok2api 替换成匿名账号标识。
- 密码使用 Resin 的代理 Token，不能使用 Tunnel Token 或 Access Secret。
- `127.0.0.1:2261` 是 Render 容器内的 cloudflared 客户端。
- 如果代理密码含 `@`、`:`、`/`、`#` 等特殊字符，嵌入 URL 前需要百分号编码。之前生成的十六进制密码无需特殊处理。

你日志中的作用域是 `grok_console`，先修改 Console 对应的节点并重新探测。Web、Build、图片等资源使用独立作用域时，按实际需求分别修改、分配出口。需要所有请求强制走 Resin 时，检查对应作用域没有配置直连回退。

保存后执行一次探测，再执行一次真实业务请求。同时查看 Resin 的请求日志或租约，确认请求确实经过 Resin。不要再把橙云域名的 `:2261` 填到这个代理框里。

## 8. 失败时按顺序排查

| 表现 | 优先检查 |
| --- | --- |
| Cloady 中 Tunnel 一直离线 | 新镜像是否已构建；环境变量是否注入；Tunnel Token 是否正确；是否允许出站 TCP 7844 |
| Cloady 等待就绪后报错 | Cloudflare 连接器日志、Token 和隧道配置；不要仅看 Resin 网页域名能否打开 |
| Render 启动时提示缺少服务令牌 | 两个 `TUNNEL_SERVICE_TOKEN_*` 是否都填写 |
| Render 提示 `connection refused 127.0.0.1:2261` | 是否启用客户端；是否仍使用旧镜像；端口是否改过；启动命令是否绕过 entrypoint |
| `websocket: bad handshake` / 401 / 403 | Tunnel 专用域名、TCP 路由、Service Auth 策略及匹配的 Service Token |
| Tunnel 在线，但源站拒绝连接 | 路由是否为 `tcp://127.0.0.1:2260`；Resin 是否已启动且默认接入点支持 SOCKS5 |
| SOCKS 用户名密码错误 | 平台名、Resin 代理 Token、URL 中特殊字符编码 |
| 已连到 Resin，但没有可用出口 | Resin 节点健康状态和平台筛选 |
| IPv4 探测正常，只有 IPv6 目标失败 | Resin 选中节点的出口是否支持目标 IPv6；这与 Render 连接隧道是不同阶段 |
| 聊天或流式连接中途断开 | WebSocket/Tunnel 重连、Render 出站连接重置、业务超时和上游状态 |

两端会共同管理 cloudflared 和主程序：任一进程退出时清理另一进程，让平台恢复容器；正常终止时向子进程发送退出信号并等待，超时才强制结束。它不能保证中途断开的生成请求可以续传，也不会自动重放已经提交的业务请求。

Cloudflare 官方提示，此类 TCP-over-WebSocket 通道不保证长期连接一直保持；特别长的流式请求仍可能中断。连接恢复并不等于原请求恢复。本次适配保留 TLS 验证，不修改业务端 IPv6 探测来掩盖网络问题。

## 9. 如果还要让本机 Clash Party 连接

Render 的 `127.0.0.1:2261` 只属于 Render 容器，你电脑上的 Clash Party 不能直接使用它。电脑也要运行一份 cloudflared Access 客户端，建立自己的本机监听，再让 Clash Party 连接这个本机监听。

例如电脑端创建监听 `127.0.0.1:2262` 时，Clash 的 SOCKS5 节点填写主机 `127.0.0.1`、端口 `2262`、用户名 `Default.clash_pc`、密码为 Resin 代理 Token。电脑端也需要 Access 授权；建议为电脑另外创建服务令牌，或使用个人浏览器登录策略。不要把 Render 的本机地址或 Tunnel 域名直接当成 SOCKS5 服务器。

## 10. 资料和验证范围

- [Cloudflare 普通橙云支持的端口](https://developers.cloudflare.com/fundamentals/reference/network-ports/)
- [Cloudflare 任意 TCP 接入](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/non-http/cloudflared-authentication/arbitrary-tcp/)
- [Cloudflare 客户端接入与 WebSocket 限制](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/non-http/cloudflared-authentication/)
- [Cloudflare Service Tokens](https://developers.cloudflare.com/cloudflare-one/access-controls/service-credentials/service-tokens/)
- [Cloudflare Tunnel 连接参数](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/run-parameters/)
- [Render 出站连接重置说明](https://render.com/docs/outbound-connection-resets)

本地检查验证配置、进程管理和打包内容。真实 Tunnel、Access 应用、DNS 及两平台之间的在线连通性，需要配置你自己的账号后验证；源码包不包含这些凭据，也没有替你在云端创建资源。
