# 客户端 Logo

用户面板「订阅链接」的三个按钮只使用本目录内置的客户端官方 Logo，不再保留旧的闪电、火箭或波浪占位图标。按钮资源统一使用透明背景 PNG。

文件名固定如下：

| 客户端 | 文件名 | 官方来源 |
|--------|--------|----------|
| ClashMeta / Mihomo | `clashmeta.png` | https://github.com/MetaCubeX/mihomo （开源项目，猫咪吉祥物） |
| Shadowrocket | `shadowrocket.png` | App Store: https://apps.apple.com/app/id932747118 （商业 App，火箭图标，商标所有：Shadow Launch Technology） |
| Surge | `surge.png` | https://nssurge.com （商业 App，商标所有：Surge Networks） |

> ⚠️ 商标提示：Shadowrocket、Surge、Mihomo 的 Logo 均为各自权利人的商标或品牌资产。本项目仅将其作为订阅目标客户端的功能性标识，不表示与相关品牌存在隶属、授权或背书关系。

Vite 会把 `web/public/` 下的文件原样发布到网站根路径，因此构建后可通过 `/logos/xxx.png` 访问；生产环境由面板托管 `web/dist`。
