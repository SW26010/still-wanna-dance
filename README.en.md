# Still Wanna Dance

[简体中文](README.md) | **English**

**Load failed? But I still wanna dance!**

Still Wanna Dance is an open-source playback tool for **VRChat WannaDance players on Windows**. Its local playback gateway handles WannaDance HTTP playback requests using upstream routing and local video caching. Keep videos on your computer, prepare upcoming songs in the room queue, and manage downloads and cached files through a simple local web console.

Just extract and run. The app lives in the Windows system tray and requires no Go, Node.js, or database installation.

[Releases](https://github.com/SW26010/still-wanna-dance/releases) · [User guide (Chinese)](docs/console.md) · [Report an issue](https://github.com/SW26010/still-wanna-dance/issues)

## Get your videos ready to dance

- **Put your cache to work**: read complete cached videos locally to avoid repeat downloads. On the first play, stream while downloading and save the video for later.
- **Prepare upcoming songs**: read the room queue from VRChat logs and prepare videos for upcoming songs, scheduling downloads around their estimated playback times.
- **See what is happening**: check active downloads, upstream speed, recent requests, and queue readiness in the console.
- **Manage your disk space**: choose a storage folder, find and delete cached videos, and check file integrity. Set a video cache limit to automatically remove lower-priority videos first.
- **Choose your connection**: select playback API parameters (Auto, node=cf, or node=nya); resource domains come from valid API responses. Application requests can use built-in encrypted DNS directly, external SOCKS5, or imported embedded Mihomo nodes. Game HTTPS passthrough always connects directly.
- **Prepare more of your library**: scan local files, verify existing videos, or download missing videos ahead of time.

## Quick start

The desktop app targets Windows x64. Choose a writable folder for the app and allow enough disk space for your videos. Chinese interface labels are included below to help you find the controls.

1. Look for a Windows portable ZIP on the [releases page](https://github.com/SW26010/still-wanna-dance/releases). Extract the entire archive, then double-click `SWD.exe`. If no release package is available yet, you can [build from source](#build-from-source).
2. The app opens a welcome page, followed by the terms for your explicit acceptance. The guide covers storage, optional import, network checks, upstream settings and local access, then lets you review and apply your changes. Keep the defaults with Next where no changes are needed. Reopen the guide from Settings or Help (使用帮助).
3. Open Settings (设置) and choose your storage folder, save, then click Restart and apply (立即重启并应用). The VRChat log folder is detected automatically by default.
4. On Get started (开始使用), click Enable game acceleration (启用游戏加速) and follow the prompts to configure the hosts entries. Windows will ask for permission when the app needs to modify the hosts file.
5. Enter WannaDance and request a song. Check the connection and request status on Get started, then use Activity (运行状态) and Local cache (本地缓存) to follow downloads and inspect cached files.

Settings → Console startup (设置 → 控制台启动) provides separate automatic startup options for the playback gateway and room queue prefetch. Both default to enabled; existing saved off settings are preserved. Changes apply the next time the app starts, and require accepted terms. Their start/stop controls are independent, and both share the local cache. If queue prefetch is stopped, enable it in Prepare songs (准备歌曲) before requesting songs. To play cached videos in the game, start the gateway and enable game access. First-time downloads still depend on the upstream server and your network. “Request received” (收到请求) means a request reached the local playback gateway; check playback in the game itself. If no requests arrive after setup, try rejoining the world or restarting the game to refresh DNS.

After accepting the terms, closing the web page leaves the app running in the system tray. Before acceptance, closing the terms page attempts to exit the app after 60 seconds; reopening it cancels the countdown. Use the tray menu to reopen the console. Quit from the page footer or the tray menu to stop all services and background tasks; hosts mappings remain in place.

**Before disabling or uninstalling the app, click Restore hosts (恢复 hosts) in the console.** Stopping the service or quitting does not automatically restore hosts entries, and leaving them in place may affect direct playback afterward.

For detailed instructions, see the [console guide](docs/console.md) and [portable app guide](docs/portable-readme.txt), both in Chinese.

## Find your way around

| What you want to do | Where to go |
| --- | --- |
| Get started and check game requests | Get started (开始使用) → Enable game acceleration (启用游戏加速) |
| View speed, download progress, and recent requests | Activity (运行状态) |
| Find cached videos, free space, and view request statistics | Local cache (本地缓存) |
| Check missing videos and download them | Prepare songs (准备歌曲) |
| Change folders, cache limits, upstream selection, or proxy settings | Settings (设置) |
| Adjust how early to prepare videos | Settings (设置) → Preparation lead M in seconds (提前准备时间 M（秒）), then save and restart |
| Start or stop queue prefetch independently | Prepare songs (准备歌曲) → Room queue prefetch (房间队列预缓存) |

Playback Gateway (播放网关) runs on your computer and handles playback requests, cache reads, upstream downloads, video delivery, and HTTPS forwarding for connections affected by game access. Local Cache (本地缓存) covers video storage, indexing, verification, and cleanup. Upstream channels (上游通道) are direct or proxy connections and their evaluation and selection. Game access (游戏接入) uses hosts mappings to direct requests to the local playback gateway. Everyday use is handled through the web console and system tray. A [standalone command-line service](docs/service.md) is also available for custom setups.

## Things to know

- **Caching serves the game's HTTP playback path.** Website playback over HTTPS is forwarded with the original encrypted connection intact and does not read from or add to the local video cache. The node=cf and node=nya playback APIs are supported; SHA is not currently supported. Resource domains are discovered from API responses, without a fixed list or node-to-domain mapping.
- **Cached videos can help with some upstream failures.** If playback URL resolution fails, the app can use the song's last confirmed local video after it passes integrity checks. This is not a full offline mode and does not cover failures during a subsequent download. See [fallback behavior](docs/service.md#本地降级边界).
- **You control cache capacity.** Video storage is unlimited by default. Downloading the full library can take substantial space, so consider setting a limit first. Files being played or downloaded can temporarily push actual disk usage above that limit.
- **The project is still evolving.** Automated tests and historical tests with the game are documented. Recent storage and playback API changes still need complete end-to-end validation with VRChat. Feedback from different networks and player environments is welcome. See [supported functionality and limitations](docs/scope.md).

## Core design: schedule data by when it is needed

The current source uses **global block scheduling driven by expected completion times**. Playback, read-ahead, queue preparation, and library downloads contribute independent demands for content blocks. Demands share one download per block and can be withdrawn independently. HTTP request positions and local media metadata estimate when each block is needed; a shared block-time estimate and preparation margin define its activation window. Eligible backlog includes blocks inside the activation window and blocks with an independent untimed demand allowing early execution. Either kind can drive gradual concurrency growth; a block actually holding up a read is admitted immediately.

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

The detailed guides below are currently in Chinese.

| Guide | Covers |
| --- | --- |
| [Local console](docs/console.md) | Web controls, system tray, game setup, and network settings |
| [Queue prefetching](docs/queue-prefetch.md) | How upcoming songs are prepared and how to configure it |
| [Global block scheduler design](docs/block-scheduler-design.md) / [Runtime rules](docs/download-concurrency.md) | Independent demands, playback deadlines, activation windows, and gradual concurrency growth |
| [Cache capacity management](docs/cache-retention.md) | Retention priority, capacity limits, and automatic cleanup |
| [Command-line service](docs/service.md) | Standalone startup, options, and cache behavior |
| [Unified storage](docs/storage.md) | Where videos, song metadata, and statistics are stored |
| [Scope and limitations](docs/scope.md) | Supported features, compatibility, and validation status |
| [Tests and validation](docs/test-results.md) | Test records and results |
| [Development guide](docs/development.md) | Development setup, code entry points, and test commands |

## Upgrading and backing up

Quit the app and back up the complete `data/` directory. For custom configurations, back up the main JSON and its instance data directory; back up any external media storage in full. Replace `SWD.exe`, `licenses/`, `README.txt` and `build-info.json` in the original app directory, preserve data and external storage, and launch the executable there. Launching from a new extraction directory selects a separate default configuration.

The current configuration format is version 3. Unversioned and version 1/2 test configurations and earlier databases are not supported or migrated; keep test data backed up and use separate new configuration and storage directories. If the app reports a newer file format, update the app instead of deleting data or editing version numbers. See the [portable upgrade guide](docs/portable-readme.txt) for the complete backup scope and steps.

## License and content use

The current distribution is offered under [GNU GPLv3](LICENSE); new original contributions use `GPL-3.0-only`. [Historical MIT grants](LICENSE-MIT) remain valid. You are welcome to use, modify, and contribute under the applicable licenses. See [licensing and source delivery](LICENSING.md) and the preserved [third-party notices](THIRD-PARTY-NOTICES.txt).

Formal releases provide a separate `*-corresponding-source.tar.gz` asset in the same GitHub Release. The package's `licenses/SOURCE.txt` contains its exact URL and checksum. Downloading source is optional for users; developers can follow the included BUILD.txt for an independent rebuild.

The software license does not grant rights to third-party videos, music, or other content. Use content only with the necessary authorization or another lawful basis, and read the [terms of use and content rights statement (Chinese)](internal/legal/TERMS.txt). This repository does not include videos, binaries from the original application, or complete game logs.
