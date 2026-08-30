# SingBox Panel

## 项目简介

SingBox Panel 是一个用于集中管理多台 Linux 服务器上官方 sing-box 的 Web 面板。

系统由中心面板、Web 管理界面和部署在节点服务器上的 Agent 组成。管理员可以在面板中管理
节点、入站、出站、路由、用户和订阅，并远程执行 sing-box 的安装、更新、启停、配置下发和卸载。

## 安装

面板服务器需要 Linux x86_64 或 arm64、systemd、已解析到服务器的域名，以及支持
WebSocket 的 HTTPS 反向代理。

```bash
curl -fsSL https://github.com/hann0w0/SingBox-Panel/releases/latest/download/install.sh | sudo bash
```

非交互安装：

```bash
curl -fsSL https://github.com/hann0w0/SingBox-Panel/releases/latest/download/install.sh | \
  sudo bash -s -- \
  --base-url panel.example.com \
  --admin admin \
  --password 'change-this-password' \
  --non-interactive
```

安装完成后，在面板中创建服务器，并在目标 VPS 上执行节点详情页生成的 Agent 安装命令。

## 卸载

卸载面板程序和 systemd 服务，保留配置与数据库：

```bash
curl -fsSL https://github.com/hann0w0/SingBox-Panel/releases/latest/download/install.sh | \
  sudo bash -s -- --uninstall --yes
```

完整删除面板、配置和数据库：

```bash
curl -fsSL https://github.com/hann0w0/SingBox-Panel/releases/latest/download/install.sh | \
  sudo bash -s -- --uninstall --purge --yes
```

节点 Agent 和 sing-box 可分别在服务器详情页中卸载，互不影响。

## 工作模式

1. 管理员通过 HTTPS 访问中心面板。
2. 每台节点服务器上的 Agent 主动通过 WSS 连接面板，节点无需开放额外的管理端口。
3. 面板通过受控指令管理 Agent；Agent 不提供任意 Shell 执行能力。
4. Agent 使用官方安装方式管理 sing-box，并在应用配置前执行 `sing-box check`。
5. 面板管理模式根据面板中的节点、协议和路由设置生成配置。
6. 原始配置模式保存并下发完整 JSON，适合使用面板暂未结构化支持的 sing-box 配置项。
7. Agent 使用安装时生成的稳定凭据连接面板，配置和程序更新采用校验、原子替换与失败回滚机制。
