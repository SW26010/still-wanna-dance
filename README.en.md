# Still Wanna Dance

[简体中文](README.md) | **English**

**Load failed? But I still wanna dance!**

Still Wanna Dance is an open-source playback tool for **VRChat WannaDance players on Windows**. Its local playback gateway handles WannaDance HTTP playback requests using upstream routing and local video storage and block caching. Keep videos on your computer, prepare upcoming songs in the room queue, and manage downloads and local video files through a simple local WebUI.

Just extract and run. The app lives in the Windows system tray and requires no Go, Node.js, or database installation.

[Releases](https://github.com/SW26010/still-wanna-dance/releases) · [User guide (Chinese)](docs/console.md) · [Report an issue](https://github.com/SW26010/still-wanna-dance/issues)

## Get your videos ready to dance

- **Reuse local videos**: read complete local videos locally to avoid repeat downloads. On the first play, stream while downloading and retain the complete video when the storage budget allows.
- **Prepare upcoming songs**: read the room queue from VRChat logs and prepare videos for upcoming songs, scheduling downloads around their estimated playback times.
- **See what is happening**: check active downloads, upstream speed, recent requests, and queue readiness in the WebUI.
- **Manage your disk space**: choose a storage folder, find and delete local videos, and check file integrity. Set a video storage budget to automatically remove lower-priority videos first.
- **Choose your connection**: select playback API parameters (Auto, node=cf, or node=nya); resource domains come from valid API responses. Application requests can use built-in encrypted DNS directly, external SOCKS5, or imported embedded Mihomo nodes. Game HTTPS passthrough always connects directly.
- **Prepare more of your library**: scan local files, verify existing videos, or download missing videos ahead of time.

## Quick start

The desktop app targets Windows x64. Choose a writable folder for the app and allow enough disk space for your videos. Chinese interface labels are included below to help you find the controls.

1. Look for a Windows portable ZIP on the [releases page](https://github.com/SW26010/still-wanna-dance/releases). Extract the entire archive, then double-click `SWD.exe`. If no release package is available yet, you can [build from source](#build-from-source).
2. The app opens a welcome page, followed by the terms for your explicit acceptance. The guide covers storage, optional import, network checks, upstream settings and local access, then lets you review and apply your changes. Keep the defaults with Next where no changes are needed. Reopen the guide from Settings or Help (使用帮助).
3. Open Settings (设置) and choose your storage folder, save, then click Restart and apply (立即重启并应用). The VRChat log folder is detected automatically by default.
4. On Video acceleration (视频加速), click Start acceleration (开始加速) and follow the prompts to configure game access. Windows will ask for permission when the app needs to modify the hosts file.
5. Enter WannaDance and request a song. Check access, requests and downloads on Video acceleration (视频加速), request history on Timeline (时间线), and local videos in Library (曲库).

Settings → General → App startup (设置 → 常规 → 程序启动) provides Open the WebUI when the app starts (启动程序时自动打开 WebUI) and Enable video acceleration when the app starts (启动程序时自动开启视频加速). Both default to enabled; saved changes apply on the next app launch. Automatic acceleration requires valid terms acceptance and completion of the initial guide; opening or refreshing the web page does not trigger it again. Queue prefetch follows the gateway by default, configured under Settings → Prefetch and downloads (设置 → 预加载与下载). First-time downloads still depend on the upstream server and your network. Connected to the background app (已连接后台程序) only means the WebUI can read app status; Request received (收到请求) only means a request reached the local playback gateway. Check playback in the game itself. If no requests arrive after setup, try rejoining the world or restarting the game to refresh DNS.

After accepting the terms, closing the web page leaves the app running in the system tray. Before acceptance, closing the terms page attempts to exit the app after 60 seconds; reopening it cancels the countdown. Use Open WebUI (打开 WebUI) in the tray menu to reopen it. Quit from the page footer or the tray menu to stop all services and background tasks; by default, quitting restores the game access configured by this app.

**Before disabling or uninstalling the app, enable and save Restore direct game access on exit (退出时恢复游戏直连) under Settings → General → Advanced game access settings (设置 → 常规 → 高级游戏接入设置), then quit and check the result.** Disabling this option leaves the mappings in place on exit, which may affect direct playback afterward.

For detailed instructions, see the [WebUI and desktop app guide](docs/console.md) and [portable app guide](docs/packaging.md#覆盖升级与备份), both in Chinese.

## Find your way around

| What you want to do | Where to go |
| --- | --- |
| Enable acceleration and check game requests | Video acceleration (视频加速) → Start acceleration (开始加速) |
| View speed, download progress, and request records | Video acceleration (视频加速); history is on Timeline (时间线) |
| Find local videos, free space, and view request statistics | Library (曲库) |
| Check missing videos and download them | Library (曲库) |
| Change folders, video storage budgets, upstream selection, or proxy settings | Settings (设置) |
| Adjust how early to prepare videos | Settings → Prefetch and downloads (设置 → 预加载与下载) → Preparation lead in seconds (提前准备时间（秒）), then save and restart |
| Configure queue prefetch to follow the gateway | Settings → Prefetch and downloads (设置 → 预加载与下载) → Enable queue prefetch with the video gateway (随视频网关启用队列预加载) |

Playback Gateway (播放网关) is a background capability of the desktop app. It handles playback requests, local video and block cache reads, upstream downloads, video delivery, and HTTPS forwarding for connections affected by game access. Library (曲库) manages complete videos retained across runs. “Cache” refers to video blocks reused within one engine run; their location is managed by the app. Upstream channels (上游通道) are direct or proxy connections and their evaluation and selection. Game access (游戏接入) uses hosts mappings to direct requests to the local playback gateway. Everyday use is handled through the WebUI and system tray. A [standalone command-line service](docs/service.md) is also available for custom setups.

## Things to know

- **Local videos and the block cache serve the game's HTTP playback path.** Website playback over HTTPS is forwarded with the original encrypted connection intact and does not read local videos or populate the block cache. The node=cf and node=nya playback APIs are supported; SHA is not currently supported. Resource domains are discovered from API responses, without a fixed list or node-to-domain mapping.
- **Local videos can help with some upstream failures.** When a song has a known mapping and an available local file, the app serves it without waiting for the background URL query. A failed query does not interrupt that local response. This is not a full offline mode; missing videos still require an upstream connection. See [local playback behavior](docs/service.md#id-播放与本地命中).
- **You control the video storage budget.** This is the local persistent video storage budget (本地视频持久化存储预算), shown as 视频存储预算 in the UI. It defaults to 0: complete videos are not retained across runs, while the runtime video block cache remains available. Choose a positive budget or unlimited storage to retain videos. Protected videos and temporary files can push actual disk usage above the budget.
- **The project is still evolving.** Automated and live game tests are documented. Remaining scenarios, including enabling access during playback, slow-network seeking to uncached data, queue handoff and multiplayer synchronization, are tracked in the [VRC backlog](docs/live-acceptance-backlog.md). Feedback from different networks and player environments is welcome. See [supported functionality and limitations](docs/scope.md).

## Core design: schedule data by when it is needed

The current source uses **global block scheduling driven by expected completion times**. Playback, queue preparation, and library downloads contribute independent demands for content blocks. Demands share one download per block and can be withdrawn independently. HTTP request positions and local media metadata estimate when each block is needed; a fixed preparation lead M defines its activation window. Eligible backlog includes blocks inside the activation window and blocks with an independent untimed demand allowing early execution. Either kind can drive gradual concurrency growth; a block actually holding up a read is admitted immediately.

See the [design, formulas, and worked example (Chinese)](docs/block-scheduler-design.md). Developers verify throughput, CPU usage, and scheduling boundaries with controlled workloads. Slow-network seeking, in-game stutter, and queue handoff with the actual VRChat player remain grouped under LA-040 / LA-041 for live acceptance.

## Build from source

To build the Windows desktop app, install Go 1.25 or later, Node.js 22+ (asset verification), Git, and PowerShell. Run these commands from the repository folder:

```powershell
.\scripts\build-desktop.ps1
.\bin\SWD.exe
```

Web assets and SQLite dependencies are compiled into the executable. Ordinary Go builds use the checked-in browser bundle. Editing the React UI requires the frontend build below; no separate runtime service is needed.

For UI changes, use the pinned pnpm version in `webui/package.json`, then run `pnpm --dir webui install --frozen-lockfile`, `pnpm --dir webui build`, and `pnpm --dir webui test`. See the [frontend guide](docs/fluent-ui-migration.md).

See the [development guide](docs/development.md) for environment setup and test commands, and the [packaging guide](docs/packaging.md) to create a distributable ZIP.

## Help make it better

Bug reports, usage feedback, feature suggestions, and pull requests are welcome. Documentation improvements, interface feedback, and testing on different networks are all useful contributions.

When [opening an issue](https://github.com/SW26010/still-wanna-dance/issues), please include the app version, Windows version, selected upstream, steps to reproduce, and expected versus actual behavior. Before sharing logs, review them and remove personal paths or other information you do not want to make public.

## More documentation

The detailed guides below are currently in Chinese. The [documentation index](docs/README.md) separates current rules, development validation, acceptance reports and historical material.

| Guide | Covers |
| --- | --- |
| [WebUI and desktop app](docs/console.md) | Web controls, system tray, game setup, and network settings |
| [Queue prefetching](docs/queue-prefetch.md) | How upcoming songs are prepared and how to configure it |
| [Global block scheduler design](docs/block-scheduler-design.md) / [Runtime rules](docs/download-concurrency.md) | Independent demands, playback deadlines, activation windows, and gradual concurrency growth |
| [Video storage budget management](docs/cache-retention.md) | Retention priority, storage budgets, and automatic cleanup |
| [Command-line service](docs/service.md) | Standalone startup, options, and local storage behavior |
| [Unified storage](docs/storage.md) | Where videos, song metadata, and statistics are stored |
| [Scope and limitations](docs/scope.md) | Supported features, compatibility, and validation status |
| [Validation process and VRC backlog](docs/live-acceptance-process.md) | Verification responsibilities, reports and remaining scenarios |
| [Development guide](docs/development.md) | Development setup, code entry points, and test commands |

## Upgrading and backing up

Quit the app and back up the complete `data/` directory. For custom configurations, back up the main JSON and its instance data directory; back up any external media storage in full. Replace `SWD.exe`, `licenses/`, `README.txt` and `build-info.json` in the original app directory, preserve data and external storage, and launch the executable there. Launching from a new extraction directory selects a separate default configuration.

The current configuration format is version 5. Unversioned and version 1–4 test configurations and earlier databases are not supported or migrated; keep test data backed up and use separate new configuration and storage directories. If the app reports a newer file format, update the app instead of deleting data or editing version numbers. See the [portable upgrade guide](docs/packaging.md#覆盖升级与备份) for the complete backup scope and steps.

## License and content use

The current distribution is offered under [GNU GPLv3](LICENSE); new original contributions use `GPL-3.0-only`. [Historical MIT grants](LICENSE-MIT) remain valid. You are welcome to use, modify, and contribute under the applicable licenses. See [licensing and source delivery](LICENSING.md) and the preserved [third-party notices](THIRD-PARTY-NOTICES.txt).

Formal releases provide a separate `*-corresponding-source.tar.gz` asset in the same GitHub Release. The package's `licenses/SOURCE.txt` contains its exact URL and checksum. Downloading source is optional for users; developers can follow the included BUILD.txt for an independent rebuild.

The software license does not grant rights to third-party videos, music, or other content. Use content only with the necessary authorization or another lawful basis, and read the [terms of use and content rights statement (Chinese)](internal/legal/TERMS.txt). This repository does not include videos, binaries from the original application, or complete game logs.
