# SingBox Panel

集中管理多台服务器上官方 sing-box 的 Web 面板。可在其中管理节点、入站、出站、路由、用户与订阅，并远程执行 sing-box 的安装、更新、启停、配置下发和卸载。

## 安装

需要 Linux x86_64 或 arm64、systemd、已解析到面板服务器的域名，以及支持 WebSocket 的 HTTPS 反向代理。

```bash
curl -fsSL https://github.com/hann0w0/SingBox-Panel/releases/latest/download/install.sh | sudo bash
```

安装脚本支持非交互安装（`--base-url`、`--admin`、`--password`、`--non-interactive`），用 `--version vX.Y.Z` 可指定版本。安装完成后在面板中创建服务器，并在目标 VPS 上执行该节点详情页生成的 Agent 安装命令。

## 卸载

```bash
# 保留配置与数据库
curl -fsSL https://github.com/hann0w0/SingBox-Panel/releases/latest/download/install.sh | sudo bash -s -- --uninstall --yes

# 连同配置与数据库一并删除
curl -fsSL https://github.com/hann0w0/SingBox-Panel/releases/latest/download/install.sh | sudo bash -s -- --uninstall --purge --yes
```

节点 Agent 与 sing-box 在对应服务器详情页单独卸载，互不影响。

## 工作模式

面板通过 HTTPS 提供管理界面，节点上的 Agent 主动以 WSS 连接面板，节点无需开放额外管理端口，也不提供任意 Shell 执行能力。Agent 以官方方式安装 sing-box，下发配置前执行 `sing-box check`，程序与配置更新采用校验、原子替换与失败回滚。

面板支持两种配置模式：管理模式根据面板中的节点、协议与路由设置生成配置；原始配置模式保存并下发完整 JSON，适合面板暂未结构化支持的 sing-box 配置项。

完整变更记录见 [CHANGELOG](CHANGELOG.md)。
