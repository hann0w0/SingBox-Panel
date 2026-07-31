# 数据库升级与恢复

Panel 使用 SQLite 保存管理员、节点、入站、用户授权和流量记录。每次需要升级数据库结构时，Panel 会在写入前创建一致性快照；迁移失败会阻止启动，避免使用半升级数据库继续运行。

Docker 默认位置：

- 数据卷：`singbox-panel_data`
- 数据库：`/data/singbox-panel.db`
- 自动备份目录：`/data/singbox-panel.db.backups/`
- 备份文件：`schema-vN-<UTC 时间>.db`

建议升级前先停止容器，并在宿主机额外复制一次数据卷。自动备份最多保留最近 5 个快照。

## 查看备份

```bash
docker stop singbox-panel
docker run --rm -v singbox-panel_data:/data:ro alpine \
  ls -lh /data/singbox-panel.db /data/singbox-panel.db.backups/
```

## 恢复指定快照

先保留故障数据库，便于排查；不要直接覆盖而不留副本：

```bash
docker run --rm -v singbox-panel_data:/data alpine \
  sh -c 'cp /data/singbox-panel.db /data/singbox-panel.db.failed-$(date -u +%Y%m%dT%H%M%SZ)'

docker run --rm -v singbox-panel_data:/data alpine \
  cp /data/singbox-panel.db.backups/schema-v3-REPLACE.db /data/singbox-panel.db

docker start singbox-panel
docker logs --tail=100 -f singbox-panel
```

把 `schema-v3-REPLACE.db` 替换为实际存在的快照文件名。恢复后，Panel 会从该版本继续执行后续迁移；如果日志提示 schema migration marked dirty，说明当前库仍是失败状态，应停止容器并重新恢复一个干净快照。

恢复动作会回退到快照创建时的节点、用户和授权数据。恢复前后都应确认反向代理和订阅域名配置没有被误改。
