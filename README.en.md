<p align="center">
  <img src="docs/images/logo-dark.svg" width="128" height="128" alt="Still Wanna Dance logo">
</p>

<h1 align="center">Still Wanna Dance</h1>

<p align="center"><strong>Load failed? But I still wanna dance!</strong></p>
<p align="center">Get the next video ready for WannaDance in VRChat on Windows.</p>

<p align="center">
  <a href="https://github.com/SW26010/still-wanna-dance/releases"><img src="https://img.shields.io/badge/Windows-x64-0078D4?style=flat-square" alt="Windows x64"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-GPLv3-80DE50?style=flat-square" alt="License: GPLv3"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-00ADD8?style=flat-square&amp;logo=go&amp;logoColor=white" alt="Go"></a>
  <a href="webui/package.json"><img src="https://img.shields.io/badge/React-20232A?style=flat-square&amp;logo=react&amp;logoColor=white" alt="React"></a>
  <a href="webui/package.json"><img src="https://img.shields.io/badge/Fluent_UI-v9-0078D4?style=flat-square" alt="Fluent UI v9"></a>
  <a href="internal/proxyengine/mihomo/README.md"><img src="https://img.shields.io/badge/Mihomo-6C5CE7?style=flat-square" alt="Mihomo"></a>
</p>

<p align="center">
  <a href="README.md">简体中文</a> · <strong>English</strong><br>
  <a href="https://github.com/SW26010/still-wanna-dance/releases"><strong>Download</strong></a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="docs/console.md">User guide</a> ·
  <a href="https://github.com/SW26010/still-wanna-dance/issues">Report an issue</a>
</p>

Still Wanna Dance (SWD) is an open-source video playback companion: **prepare the room queue ahead of time and reuse local files on repeat plays** to reduce downloading and waiting. Manage it in your browser while the app runs in the Windows system tray.

A **[global block scheduler driven by playback timing](docs/block-scheduler-design.md)** prepares the data needed soonest and shares downloads between playback and preloading. Connection measurements guide path selection, while song demand guides local video retention. The app combines a **Go** backend, a **React + Fluent UI v9** WebUI, and embedded **Mihomo** proxy support.

## What you can do

- **Prepare videos ahead of time**: preload the room queue and stream while downloading.
- **Manage your library**: search, preview, favorite, or block songs; import existing videos and download missing ones.
- **Plan your storage**: give frequent picks and favorites priority within your budget. You can also use your VRCX playback history to prioritize videos for download and retention.
- **Flexible connections**: by default, SWD dynamically selects CF IP addresses and downloads in parallel, which works for most use cases. You can also switch to built-in Mihomo (Clash) or an external SOCKS5 service.

## A look inside

**Acceleration: connection status, download progress, and upcoming video demands.**

![Dark WebUI acceleration page showing game connection status, a video block timeline, the current song ID and room playback progress, and preloading for the next three songs](docs/images/webui-acceleration.png)

<details>
<summary>More screenshots: library and network</summary>

**Library: browse song covers, search, manage favorites, preview videos, and manage local files.**

![Dark WebUI library page showing the default cover view, song search, favorites, and per-song action menus](docs/images/webui-library.png)

**Network: the Current connection card shows direct routes for the song catalog, playback URL resolution, and video downloads.**

![Three route lists on the Current connection card showing full catalog fetch time, time to first byte for playback URLs, and time to download a full 1 MiB video block](docs/images/webui-network.png)

</details>

These screenshots show the current WebUI with public song metadata and available covers for top-ranked songs in the bundled default scores. Missing covers use the app's bundled category artwork. The Network screenshot uses IPs from the bundled Cloudflare list. Timings, speeds, download progress, favorites, and local availability use sample data. The interface supports light, dark, and system themes. Screenshots and detailed guides are currently in Chinese.

## Quick start

1. **Download and run.** Get the Windows x64 portable ZIP from [Releases](https://github.com/SW26010/still-wanna-dance/releases), extract the whole package to a writable folder, and run `SWD.exe`.
2. **Read and accept the terms.** The app opens the WebUI by default and prepares your configuration, database, and video folders automatically.
3. **Start acceleration and request a song.** Click `加速 → 开始加速` and approve the Windows prompt if it appears. Enter WannaDance, set the CDN to **Auto, CF, or HKG** in the room settings, then request a song.

**Why does it need administrator permission?** SWD modifies the Windows hosts file to route WannaDance playback requests through your PC. By default, stopping acceleration or exiting normally restores the routing SWD configured; restoration failures are reported. See [game routing and restoration](docs/console.md#hosts).

**Want to keep videos for next time?** By default, SWD uses temporary blocks without retaining ordinary complete videos. You can choose a storage budget or unlimited retention under Settings → Storage (`设置 → 存储`), save, and restart when prompted. You can try acceleration with the defaults first.

Closing the browser leaves acceleration and queue preloading running. Reopen the WebUI from the tray, or choose `退出程序` to exit.

**How do I restore the game's direct connection?** Before you stop using or uninstall SWD, open About → Help → Backups and upgrades (`关于 → 使用帮助 → 备份与升级`) and click Restore direct connection (`恢复游戏直连`). This stops acceleration and works even if you disabled automatic restoration. Exit the app once it confirms that routing was restored or that no restoration was needed. See [game routing and restoration](docs/console.md#hosts).

## Common questions

**Which CDN should I select in the game?** Choose **Auto, CF, or HKG** in WannaDance to use preloading and local video reuse for in-game HTTP playback. SHA is unsupported. HTTPS video streams pass through as encrypted connections to the origin. See [supported scope](docs/scope.md) for compatibility details.

**Which videos can play from my PC?** When SWD has a usable local video and knows which song it belongs to, it can serve the video directly. Missing videos need a network connection, and other game features have their own network requirements.

**How can I confirm the game is using SWD?** Request a song in the game, then click Recent request (`最近请求`) on the Acceleration page to open History. Look for a new entry matching the time and song. Video Demands (`视频需求`) also lists songs being preloaded from the room queue. The backend connection badge shows whether the WebUI can reach SWD. The game connection badge shows whether routing is configured. If no new request appears, check that the room CDN is set to Auto, CF, or HKG, then try rejoining the world or restarting VRChat. If requests arrive but downloads are slow, run a speed test on the Network page.

**Which files count toward the video budget?** The budget governs retention of complete videos, including favorites. When priority retention is enabled, favorites are kept first within the same budget but can still be removed when space is insufficient. Temporary blocks, the database, and videos in use can take disk usage above that budget. “Do not retain” still permits temporary disk writes.

**How much space should I allocate?** Choose a budget that fits your disk. With retention enabled, SWD gives frequently requested videos priority so they can play locally next time. In a backtest on historical song requests, scoring with a 60-day half-life covered about 51% of each day’s distinct songs on average at 20 GiB, and about 68% at 50 GiB. [See results for other budgets](docs/research/cache-priority.md)

In a game test with upstream bandwidth capped at 1 Mbps, the player took over the existing download while the next song was still preloading. The song changed and played normally. Game tests also cover cold playback, local hits, multiplayer synchronization, and routing restoration on exit. [Queue handoff test](docs/acceptance/live-acceptance-20261028-01.md) · [Game validation records](docs/live-acceptance-backlog.md)

## Help and contribute

- [User guide](docs/console.md) · [Queue preloading](docs/queue-prefetch.md) · [Upgrades and backups](docs/packaging.md#覆盖升级与备份)
- [Report an issue or suggest a feature](https://github.com/SW26010/still-wanna-dance/issues): include the app version, Windows version, steps, and expected versus actual behavior. Download a diagnostic ZIP under About → Help → Report an issue (`关于 → 使用帮助 → 反馈问题 → 下载诊断包`); it includes request history and SWD/VRChat logs for the selected period. Check the contents for private paths, player and room information, links, and credentials before sharing; the ZIP is not automatically redacted.
- [All documentation](docs/README.md)

<a id="build-from-source"></a>
<a id="building-from-source"></a>

Code, documentation, and user feedback are welcome. For source builds, tests, and packaging, see the [development guide](docs/development.md) and [packaging guide](docs/packaging.md).

## License

Current distributions use [GNU GPLv3](LICENSE); new project contributions use `GPL-3.0-only`. [Historical MIT grants](LICENSE-MIT) remain valid. See [LICENSING.md](LICENSING.md) and [third-party notices](THIRD-PARTY-NOTICES.txt). Formal releases include corresponding source in the same Release; ordinary use does not require downloading it.

The software license does not grant rights to third-party video or music. Use content only with the necessary authorization or another lawful basis, and read the [terms and content rights notice](internal/legal/TERMS.txt). This repository contains no videos or complete game logs.
