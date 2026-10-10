# LoL 高光剪辑

用 [MyGo](https://mygo.egoist.dev/) 写的桌面应用：选一场英雄联盟比赛录像，切成 20 秒的小段，交给 Cloudflare **Clef-omni**（兼容 Jev 决策 API，支持带声音的视频）逐段判断保留还是丢弃，再用 ffmpeg 把保留的片段拼成精彩集锦。

## 依赖

- Go 1.27+（`GOTOOLCHAIN=auto` 会自动下载）、Bun、ffmpeg/ffprobe（`brew install ffmpeg`）
- Cloudflare 凭证，二选一：
  - 环境变量 `CLOUDFLARE_ACCOUNT_ID` + `CLOUDFLARE_API_TOKEN`（Workers AI 权限），走 REST API
  - 已登录的 `cf` CLI（`cf auth login`），应用会调用 `cf ai run @cf/cloudflare/clef-omni`

## 运行

```bash
bun install
bun run dev        # 开发模式（Vite 端口 5188）
bun run build      # 打包 .app / .dmg 到 build/
```

命令行版本（不开窗口，方便批量或调试）：

```bash
go run ./cmd/lolcut -o out.mp4 match.mp4
go run ./cmd/lolcut -analyze-only -keep 0.9 -score 2.5 match.mp4
```

## 流程

1. `ffprobe` 读时长，按段长切分。
2. 每段用 ffmpeg 转成 360p/4fps/单声道小文件（约 0.5 MB），base64 发给 Clef-omni，问三个问题：
   - `keep`（noul）：该不该进集锦
   - `score`（score 0–4）：精彩程度
   - `scene`（choice）：BP、解说台、对线、小规模交战、团战、资源、回放、结束
3. 保留规则：`keep ≥ 0.9` 或 `score ≥ 2.5`，并补齐两段保留之间的单个空隙。界面里可以拖阈值实时重选，也可以单击两次手动切换某一段。
4. 判定结果缓存在 `~/Library/Caches/<identifier>/`，改阈值不会重新调用模型。
5. 按保留区间从原片重新编码切出（准确到帧边界），用 concat 拼接，输出到 `~/Movies/LoL Highlights/`。

## 实测

LCK 决赛 GEN vs HLE，47:39 / 367 MB：143 段全部判定成功，约 75 万输入 token（约 $0.11），4 路并发 2–3 分钟；保留 22 段，生成 7:20 的集锦，剪辑约 20 秒。

## 自测

`LOL_HIGHLIGHTS_OPEN=<视频> LOL_HIGHLIGHTS_SELFTEST=<截图.png> bun run dev` 会自动打开视频、点击分析和生成，并保存窗口截图。
