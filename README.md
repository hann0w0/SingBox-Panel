<div align="center">

# SingBox Panel

**通过 Agent 集中管理多台服务器上的 sing-box**

</div>

## 安装

准备一个已解析到面板服务器的域名，并为域名配置 HTTPS 反向代理。随后在 Linux VPS 上执行：

```bash
curl -fsSL https://raw.githubusercontent.com/hann0w0/SingBox-Panel/refs/heads/main/install.sh | sudo bash
```

首次安装会依次询问：

- 本机反向代理端口，默认 `32334`
- 管理员账号，默认 `admin`
- 管理员密码，留空时自动生成
- 面板与 Agent 通信域名，例如 `panel.example.com`

脚本会自动安装或检查 Docker Engine，并从 Docker Hub 拉取预构建镜像；无需 Docker Compose、Git、Go、Node 或本地编译环境。

面板只监听 `127.0.0.1`，不会直接暴露到公网。请将 Caddy、Nginx 或 OpenResty 反向代理到安装时设置的本机端口，并确保：

- 域名使用有效的 HTTPS 证书
- 反向代理支持 WebSocket
- 公网只开放 `80/443`，不要开放面板本机端口

安装配置默认保存在 `/opt/singbox-panel/deploy/.env`，面板数据保存在 Docker 数据卷 `singbox-panel_data` 中。容器日志会自动轮转，单文件最大 10 MB，保留 3 份。

### 更新

再次执行安装命令即可更新。脚本会沿用现有配置并拉取最新镜像；新容器健康检查失败时，会自动恢复旧容器，不会删除现有数据。

```bash
curl -fsSL https://raw.githubusercontent.com/hann0w0/SingBox-Panel/refs/heads/main/install.sh | sudo bash
```

### 重新配置

需要修改本机端口或面板域名时执行：

```bash
curl -fsSL https://raw.githubusercontent.com/hann0w0/SingBox-Panel/refs/heads/main/install.sh | sudo bash -s -- --configure
```

管理员账号和密码只用于初始化空数据库。已有面板请在面板内修改管理员信息。

### 卸载

卸载面板并保留配置和数据库：

```bash
curl -fsSL https://raw.githubusercontent.com/hann0w0/SingBox-Panel/refs/heads/main/install.sh | sudo bash -s -- --uninstall --yes
```

彻底卸载并删除数据库（不可恢复）：

```bash
curl -fsSL https://raw.githubusercontent.com/hann0w0/SingBox-Panel/refs/heads/main/install.sh | sudo bash -s -- --uninstall --purge --yes
```

## 工作方式

```mermaid
flowchart LR
    Browser["管理员 / 用户"] -->|"HTTPS"| Panel["SingBox Panel"]
    Agent1["服务器 1 · Agent"] -->|"WSS 主动连接"| Panel
    Agent2["服务器 2 · Agent"] -->|"WSS 主动连接"| Panel
    AgentN["更多服务器 · Agent"] -->|"WSS 主动连接"| Panel
    Agent1 --> SingBox1["官方 sing-box"]
    Agent2 --> SingBox2["官方 sing-box"]
    AgentN --> SingBoxN["官方 sing-box"]
```

1. 面板部署在中心服务器上，通过 HTTPS 域名提供管理、订阅和 Agent 通信入口。
2. 在节点详情页复制安装命令，将 Agent 安装到被控服务器。
3. Agent 主动通过 WSS 连接面板，因此被控服务器不需要额外开放 Agent 管理端口。
4. 管理员在面板中维护节点、协议、出站和路由；配置下发前由 Agent 调用官方 `sing-box check` 校验。
5. 校验通过后 Agent 才更新配置并管理 sing-box 服务；校验失败时保留原配置，避免错误配置中断服务。
6. Agent 仅接受预设的管理指令，不提供任意 Shell 命令执行能力；安装和升级 sing-box 时使用官方发布版本。

面板支持两种配置方式：

- **面板管理**：由面板根据协议、出站和路由规则生成完整的 `config.json`。
- **原始配置**：保存管理员编辑或导入的完整 JSON，面板无法识别的字段也会原样保留。
