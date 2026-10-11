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
2. 每段用 ffmpeg 转成 480p/4fps/单声道小文件，base64 发给 Clef-omni，一次问六个问题：
   - `keep`（noul）：该不该进集锦
   - `kill`（noul）：有没有英雄被击杀
   - `opening` / `ending`（noul）：是不是比赛开局 / 结尾
   - `score`（score 0–4）：精彩程度
   - `scene`（choice）：BP、解说台、对线、小规模交战、团战、资源、回放、结束
3. 保留规则：`keep ≥ 0.9`、`score ≥ 2.5` 或 `kill ≥ 0.4` 任一满足就保留；另外保留开局和结尾（各取最靠前 / 最靠后的一串命中段，避免中途误判），并补齐两段保留之间的单个空隙。界面里可以拖阈值实时重选，也可以单击两次手动切换某一段。
4. 判定结果缓存在 `~/Library/Caches/<应用名>/`，改阈值不会重新调用模型；问题改了（`QuestionsVersion`）会自动重新判定。
5. 按保留区间从原片重新编码切出（准确到帧边界），用 concat 拼接，输出到 `~/Movies/LoL Highlights/`。

## 实测

LCK 决赛 GEN vs HLE，47:39 / 367 MB：143 段全部判定成功，约 89 万输入 token（约 $0.13），4 路并发约 2.5 分钟。对照画面顶部比分，15 个有击杀的片段在 `kill ≥ 0.4` 下全部保留（0.5 会漏 1 个）。最终保留 66 段，生成 22:00 的集锦，剪辑约 1.5 分钟。

## 自测

`LOL_HIGHLIGHTS_OPEN=<视频> LOL_HIGHLIGHTS_SELFTEST=<截图.png> bun run dev` 会自动打开视频、点击分析和生成，并保存窗口截图。
