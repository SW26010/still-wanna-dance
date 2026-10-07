# Still Wanna Dance

[简体中文](README.md) | **English**

**Load failed? But I still wanna dance!**

Still Wanna Dance is an open-source local video cache for **VRChat WannaDance players on Windows**. Keep videos on your computer, prepare upcoming songs in the room queue, and manage downloads and cached files through a simple local web console.

Just extract and run. The app lives in the Windows system tray and requires no Go, Node.js, or database installation.

[Releases](https://github.com/SW26010/still-wanna-dance/releases) · [User guide (Chinese)](docs/console.md) · [Report an issue](https://github.com/SW26010/still-wanna-dance/issues)

## Get your videos ready to dance

- **Put your cache to work**: read complete cached videos locally to avoid repeat downloads. On the first play, stream while downloading and save the video for later.
- **Prepare upcoming songs**: read the room queue from VRChat logs and prefetch valid songs in the first 3 queue positions by default. Adjust the number to suit your needs.
- **See what is happening**: check active downloads, upstream speed, recent requests, and queue readiness in the console.
- **Manage your disk space**: choose a storage folder, find and delete cached videos, and check file integrity. Set a video cache limit to automatically remove lower-priority videos first.
- **Choose your connection**: select playback API parameters (Auto, node=cf, or node=nya); resource domains come from valid API responses, with either a direct connection using built-in encrypted DNS or a SOCKS5 proxy.
- **Prepare more of your library**: scan local files, verify existing videos, or download missing videos ahead of time.

## Quick start

The desktop app targets Windows x64. Choose a writable folder for the app and allow enough disk space for your videos. Chinese interface labels are included below to help you find the controls.

1. Look for a Windows portable ZIP on the [releases page](https://github.com/SW26010/still-wanna-dance/releases). Extract the entire archive, then double-click `still-wanna-dance-console.exe`. If no release package is available yet, you can [build from source](#build-from-source).
2. The app opens its local web console. On first launch, read and accept the terms of use.
3. Open Settings (设置) and choose your storage folder, save, then click Restart and apply (立即重启并应用). The VRChat log folder is detected automatically by default.
4. On Home (首页), click Enable game acceleration (启用游戏加速) and follow the prompts to configure the hosts entries. Windows will ask for permission when the app needs to modify the hosts file.
5. Enter WannaDance and request a song. Check the connection and request status on Home, then use Monitor (监控) and Cache (缓存) to follow downloads and inspect cached files.

To prepare upcoming songs, enable Room queue prefetch (房间队列预缓存) in Settings before requesting songs. Its start/stop controls are independent of the CDN. First-time downloads still depend on the upstream server and your network. “Request received” (收到请求) means a request reached the local service; check playback in the game itself. If no requests arrive after setup, try rejoining the world or restarting the game to refresh DNS.

Closing the web page leaves the app running in the system tray. Use the tray menu to reopen the console. Quit from Home or the tray menu to stop all services and background tasks; hosts mappings remain in place.

**Before disabling or uninstalling the app, click Restore hosts (恢复 hosts) in the console.** Stopping the service or quitting does not automatically restore hosts entries, and leaving them in place may affect direct playback afterward.

For detailed instructions, see the [console guide](docs/console.md) and [portable app guide](docs/portable-readme.txt), both in Chinese.

## Find your way around

| What you want to do | Where to go |
| --- | --- |
| Get started and check game requests | Home (首页) → Enable game acceleration (启用游戏加速) |
| View speed, download progress, and recent requests | Monitor (监控) |
| Find cached videos, free space, and view request statistics | Cache (缓存) |
| Scan the library, verify files, and download missing videos | Library & downloads (曲库与下载) |
| Change folders, cache limits, upstream selection, or proxy settings | Settings (设置) |
| Adjust how many queue positions to prefetch | Settings (设置) → Queue prefetch count (队列预缓存数量), then save and restart |
| Start or stop queue prefetch independently | Settings (设置) → Local CDN (本地 CDN) → Room queue prefetch (房间队列预缓存) |

“Local CDN” in the console refers to the cache service running on your computer. Everyday use is handled through the web console and system tray. A [standalone command-line service](docs/service.md) is also available for custom setups.

## Things to know

- **Caching serves the game's HTTP playback path.** Website playback over HTTPS is forwarded with the original encrypted connection intact and does not read from or add to the local video cache. The node=cf and node=nya playback APIs are supported; SHA is not currently supported. Resource domains are discovered from API responses, without a fixed list or node-to-domain mapping.
- **Cached videos can help with some upstream failures.** If playback URL resolution fails, the app can use the song's last confirmed local video after it passes integrity checks. This is not a full offline mode and does not cover failures during a subsequent download. See [fallback behavior](docs/service.md#本地降级边界).
- **You control cache capacity.** Video storage is unlimited by default. Downloading the full library can take substantial space, so consider setting a limit first. Files being played or downloaded can temporarily push actual disk usage above that limit.
- **The project is still evolving.** Automated tests and historical tests with the game are documented. Recent storage and playback API changes still need complete end-to-end validation with VRChat. Feedback from different networks and player environments is welcome. See [supported functionality and limitations](docs/scope.md).

## Build from source

To build the Windows desktop app, install Go 1.25 or later, Git, and PowerShell. Run these commands from the repository folder:

```powershell
.\scripts\build-desktop.ps1
.\bin\still-wanna-dance-console.exe
```

Web assets and SQLite dependencies are compiled into the executable. There is no separate frontend build or database service to set up.

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
| [Cache capacity management](docs/cache-retention.md) | Retention priority, capacity limits, and automatic cleanup |
| [Command-line service](docs/service.md) | Standalone startup, options, and cache behavior |
| [Unified storage](docs/storage.md) | Where videos, song metadata, and statistics are stored |
| [Scope and limitations](docs/scope.md) | Supported features, compatibility, and validation status |
| [Tests and validation](docs/test-results.md) | Test records and results |
| [Development guide](docs/development.md) | Development setup, code entry points, and test commands |

## Upgrading from StepStash

Still Wanna Dance is the project's new name. If your StepStash installation already uses the current unified storage format, quit the old app, back up your configuration and storage folder, and place the new executable in the original app folder to keep using your settings and cache. You will need to accept the updated terms after upgrading.

Migration from earlier storage formats or the original application's song library is not currently supported. Use a new, empty storage folder and keep your old data as a backup. See the [portable upgrade guide](docs/portable-readme.txt) for details.

## License and content use

This project is released under the [MIT License](LICENSE). You are welcome to use, modify, and contribute to it. Dependency licenses are included in the [third-party notices](THIRD-PARTY-NOTICES.txt).

The software license does not grant rights to third-party videos, music, or other content. Use content only with the necessary authorization or another lawful basis, and read the [terms of use and content rights statement (Chinese)](internal/legal/TERMS.txt). This repository does not include videos, binaries from the original application, or complete game logs.
