# 歌曲子类别封面来源与映射核对

来源核对日期：2026-11-12；处理、用户确认及接入验证更新至 2026-11-13。前期完成来源调查、用户截图的透视校正与类别映射，并比较网络同版高清图、保存分辨率与格式，未从游戏缓存导出贴图。用户随后确认保持 4:3，后续产品接入及补充高清默认图见下节；前期素材交付记录保留当时边界。

目标是 WannaDance 点歌界面实际显示的方形子类别图。用户后续提供的七张 4K 截图已覆盖本次清单全部 50 个可见类别；前期方形成品共 44 张，含 16 张同版官方图片，后续 4:3 成品的来源调整见下节。游戏工程中的原始图集仍未取得，截图校正版不冒充原始纹理。此前原预加载器的 18 类图片与目标不同，仅作为调查历史保留。

## 产品接入：4:3 类别默认封面

用户最终确认采用 **320 × 240、WebP q50**。44 张运行成品共 **215,088 字节（约 210 KiB／0.21 MiB）**，覆盖 50 个可见子类别；类别名称和共享关系见 `webui/src/category-covers.ts`。Just Dance Solo 采用下文核对的 Xbox 官方横版，其余 43 张完整居中，使用同图缩放填充、15px 高斯模糊并将背景亮度降至 55% 后补齐侧边；该半径与早期 512px 成品的 24px 按比例对应。模糊只作用于背景，前景没有裁切或拉伸；全部提前合成，浏览器无需实时执行模糊。相对初次 838,886 字节的运行版减少约 74%。此前试用的 5.51 MiB 无损运行版已撤回，原图与无损母版继续单独归档。

用户补充的 `file_2a34df08-ac8f-49ac-b55a-0c05727a5afb.png` 为 2048 × 2048、不透明 RGBA，与游戏中的通用舞者图一致，比此前截图版本更清楚；源文件 SHA-256 为 `adf393d4a2c6b6a29db5fad5f0375d47cd05ebd42d225d3c967ecb13bfd990e0`。运行成品改用该文件，7 个共享类别及未知类别共用同一份 `default.webp`。来源记录为用户提供的房间／社群美术，不根据本地目录名推断独立的公开来源或授权。

产品在加载中、缺图、请求失败或浏览器解码失败时显示类别图，真实曲目封面成功显示后覆盖它；已有曲目图更新失败仍保留原图。静态素材随程序内嵌，通过已有 WebUI 素材路由提供，不额外向外部网站请求图片，也不改变曲目封面的四路调度、缓存及更新计数。现行交互依据为[曲库封面视图](../webui-design.md#曲库封面视图)；每张成品的来源、处理参数、字节数与哈希见[成品来源清单](category-cover-assets-20261112.json)。

最终 q50 版通过 19 项 Edge 封面回归及隔离真实 Console 联调，全部 44 张完整解码、输入再生成的字节一致性、50 类映射与总量预算核对通过。早期 q88／无损候选的验证作为历史保留；现行范围见[WebUI 验证记录](../validation/webui-validation.md#2026-11-13采用-q50-类别封面)。

## 母版归档与受控导出

原图／无损母版作为后续编辑依据，4:3 成品作为程序运行素材，两者分别保留。运行图从校正后的 PNG 母版或原始 JPEG／PNG 生成，而不是再读前期 384 × 384 有损 WebP；只新增最后一次 q50 有损编码。完整质量保障来自保留母版、避免重复有损编码和核对最终展示，而非将无损母版体积转嫁给 EXE。无损 WebP 仍保留为对照候选；其 `quality=100` 表示编码努力程度，依据见 [Pillow 的 WebP 保存参数](https://pillow.readthedocs.io/en/stable/handbook/image-file-formats.html#webp)。

处理规则由 [prepare-category-covers.py](../../scripts/prepare-category-covers.py) 和[成品清单](category-cover-assets-20261112.json)维护：输入逐张核对 SHA-256；截图透视校正结果保存为无损 PNG 母版，网络原文件原样归档；最终缩放、背景模糊及合成在内存中完成，整图只编码保存一次。今后修改分辨率、背景或构图仍从这些输入重新生成，不在已输出的成品上反复加工。`--check` 比较全部生成字节；无损实验输出另作逐像素核对，有损运行图按实际解码、展示与编码参照检查。Pillow 13.0.0、libwebp 1.6.0、脚本哈希与输入哈希均已记录，生成脚本与来源清单也纳入 WebUI 构建新鲜度检查。

无损保存不会撤销必要的缩放／透视重采样，也不能修复网络 JPEG 已有的损失。本轮核对 16 张官方原文件与对应 PNG 母版，解码像素完全一致；26 张截图采用的 PNG 与第一次透视校正母版像素完全一致；其余两张直接使用用户 PNG 和官方横版 JPEG 原字节。母版再次保存为 PNG 没有引入新的压缩损失。

| 最终编码 | 44 张总字节 | 约 MiB | 220px 卡片下前景平均 SSIM | 与未压缩合成画面逐像素相同 |
| --- | ---: | ---: | ---: | ---: |
| 初次 q88 有损 WebP | 838,886 | 0.80 | 0.996441 | 0 / 44 |
| q95 有损 WebP | 1,356,712 | 1.29 | 0.997920 | 0 / 44 |
| 已撤回的无损运行候选 | 5,779,122 | 5.51 | 1.000000 | 44 / 44 |

此表保留早期同为 512 × 384 的编码比较，指标只比较编码前后合成画面，不表示恢复游戏原纹理；最终 320 × 240 q50 的选择见下方比较记录。原图／无损母版留在程序外。运行类别素材合计限定 **1 MiB**，由 [WebUI 构建检查](../../scripts/check-webui.mjs)按实际文件大小阻断超限，保留逐张 256 KiB 精确登记；单图未超限不能替代总量约束。逐图测量见[编码比较](category-cover-encoding-20261113.json)，对照预览位于忽略目录 `artifacts/category-cover-implementation/quality/comparison.png`。

输入已另外归档为本机 `artifacts/category-cover-implementation/category-cover-sources-20261113.zip`，共 20,772,944 字节，SHA-256 为 `22ea4a1b6dfe7e600a500b5b147e55f7a91e76f3c960fa2c860a508b0735d910`，ZIP CRC 核对通过。包内含 44 份精确生成输入、16 份网络原文件、来源清单与生成脚本；没有完整私人截图或游戏日志。完整原始截图继续保留在用户原位置，不以成品替代。该输入包不随程序分发，也不进入 Git；普通构建仍直接使用已登记的运行成品。

包内清单保留无损候选试验时的冻结配方，现行运行参数以仓库成品清单的 320 × 240 q50 为准。将输入包解压后，从仓库运行以下命令；默认读取仓库清单，仅从解压目录取得经哈希核对的原图／母版（需要上述 Pillow 版本和相同 libwebp）：

```powershell
python scripts/prepare-category-covers.py --sources <解压目录> --check
python scripts/prepare-category-covers.py --sources <解压目录> --output <独立导出目录>
```

导出命令只写独立目录和校验报告，不自动修改产品素材、类别映射或产物登记。确认导出质量后再替换运行成品并构建；原图和母版保持不变。

## 紧凑版本的进一步平衡比较

用户追问 0.80 MiB 是否已是最优后，再次从同一批经哈希核对的 44 份母版生成 12 组候选：宽度 384／448／512、质量 75／80／85／88，均为 4:3 WebP、method 6。背景模糊半径随输出宽度同比缩放，保持视觉强度；该阶段没有更改来源、类别映射或产品。512 × 384 q88 输出与当时的 44 张运行成品逐字节一致。

本轮直接从母版生成 220／440／688 物理像素卡片下的前景参照，分别对应常见 220 CSS px 的 1x／2x，以及约 344 CSS px 窄屏单卡的 2x；不会用已有压缩图当作参照。以下 SSIM 只评估前景，避免模糊背景抬高平均分。本轮与上节编码对比的参照生成方式略有不同，指标仅在各自表内比较。[完整候选摘要](category-cover-balance-20261113.json)保留 12 组结果及低分样本。

| 候选 | 44 张总字节 | 约 MiB | 比该阶段 q88 减少 | 220px 前景平均 SSIM | 440px 前景平均 SSIM |
| --- | ---: | ---: | ---: | ---: | ---: |
| 该阶段基准 512 × 384 q88 | 838,886 | 0.80 | — | 0.996464 | 0.989409 |
| 512 × 384 q85 | 719,346 | 0.69 | 14.25% | 0.995574 | 0.986973 |
| **512 × 384 q80** | **586,584** | **0.56** | **30.08%** | **0.994080** | **0.983098** |
| 448 × 336 q85 | 615,124 | 0.59 | 26.67% | 0.994716 | 0.983620 |
| 384 × 288 q80 | 416,038 | 0.40 | 50.41% | 0.991390 | 0.974189 |

目视比较包括 220px 实际显示大小的 Solo 标题、Kkardio 标志、两张人物图和通用舞者图，以及原生像素下的标题、头像和背景细节。q80 在常见卡片大小下与 q88 差异很小，放大后仍能看到轻微细节损失；不能称为无损。减小到 384 × 288 会进一步影响高 DPI 细节。此阶段优先预留 2x 桌面显示余量，因此将 512 × 384 q80 列为候选；随后用户要求按真实卡片尺寸判断，更小分辨率的现行建议见下节。两轮均未自动替换产品素材，也不声称存在普适最优值。

该阶段只比较候选，运行素材保持 q88、838,886 字节，未自动替换；最终经用户同意采用 q50，见末节。完整逐图数据、候选文件和无损 PNG 对照位于忽略目录 `artifacts/category-cover-implementation/balance/`；该阶段没有重新运行产品或游戏验收。

## 实际卡片尺寸与更小的导出目标

在 Edge 157.0.4341.46 中加载现行生产界面，以 50 首内存示例曲目保持真实分页和滚动布局；测量 33 个窗口／导航／DPR 组合，另外对 320～2560 CSS px 窗口宽度按 16px 间隔扫描 141 次。数据与边界见[实际尺寸记录](category-cover-size-20261113.json)，没有启动日常后台或改动用户曲库。

卡片宽度不是固定值。网格最小列宽为 180 CSS px，余宽由各列均分，间距为 12px。下表为默认导航状态下的实测值，约取整；封面区域为 4:3，卡片底部标题／操作栏另占约 40 CSS px 高度。

| 浏览器可用宽度（CSS px） | 列数 | 封面区域（CSS px） | 整卡（CSS px） |
| ---: | ---: | ---: | ---: |
| 1280 | 5 | 200 × 150 | 200 × 190 |
| 1366 | 5 | 217 × 163 | 217 × 203 |
| 1440 | 6 | 192 × 144 | 192 × 184 |
| 1920 | 8 | 201 × 150 | 201 × 190 |
| 768 | 2 | 263 × 197 | 263 × 237 |
| 390 | 1 | 343 × 257 | 343 × 297 |

浏览器 DPR 为 1.25／1.5／2 时，CSS 尺寸不变，但图片需要的物理像素随之增加：1280px 窗口内约 200 × 150 的封面，分别占约 251 × 188／301 × 226／401 × 301 物理像素。1366px 窗口内较宽的卡片在 1.5x 时约需 327 × 245，在 2x 时约需 435 × 327。不能直接将 CSS 像素当作所有缩放情况下的源图片尺寸。

宽度扫描测得封面约 180～369 CSS px；网格由单列切为两列的理论门槛为 `180 × 2 + 12 = 372px`，因此 1x 下最大单列封面接近但小于 372px。384 × 288 可覆盖这一 1x 范围，也可覆盖大多数常用桌面窗口在 1.25x／1.5x 时的像素需求；它不能保证窄屏 2x 下完全不用放大。

再由同一批 44 份母版输出更小的 q80 候选，完整解码并比较 200px／300px／440px／688px 前景参照：

| 4:3 导出尺寸 | 质量 | 44 张总字节 | 约 MiB | 比该阶段 q88 减少 |
| --- | ---: | ---: | ---: | ---: |
| 256 × 192 | 80 | 256,816 | 0.24 | 69.39% |
| 320 × 240 | 80 | 336,130 | 0.32 | 59.93% |
| **384 × 288** | **80** | **416,038** | **0.40** | **50.41%** |
| 512 × 384 | 80 | 586,584 | 0.56 | 30.08% |
| 该阶段基准 512 × 384 | 88 | 838,886 | 0.80 | — |

已查看 200 CSS px 卡片在 150% 缩放下的标题、标志与人物对照。320 × 240 可用于更偏向体积的桌面目标，但放大与压缩的细节损失比 384 档更明显；256 档对较大卡片和高 DPI 的余量不足。此阶段提出 384 × 288 q80 的保守建议；随后用户目视接受 320 × 240 q80，并要求更强压缩，因此下一节以该已接受版本为基准继续比较。各档都保持一次从母版编码，不读取旧有损成品再压缩。

该阶段只测量和比较，运行素材保持 512 × 384 q88，全部哈希复核未变；最终经用户同意采用 q50，见末节。原始测量、较小候选和无损 PNG 对照位于忽略目录 `artifacts/category-cover-implementation/card-size/`；该阶段夹具服务器和隔离浏览器已关闭，未重复运行不受影响的产品行为验收。

## 用户接受 320 × 240 后的更强压缩比较

用户表示看不出 320 × 240 q80 与此前版本的差别，进一步要求提高压缩强度。本轮固定该尺寸和构图，只测试 q80／70／60／50／40／30／20。44 张均从同一份未压缩的 320 × 240 合成画面直接编码，不将 q80 成品再压缩。q80 输出逐字节匹配前一轮已接受的基准；所有 308 张候选均完整解码核对尺寸。

| 质量 | 44 张总字节 | 约 KiB | 比 320 × 240 q80 减少 |
| ---: | ---: | ---: | ---: |
| 80，已接受的基准 | 336,130 | 328.3 | — |
| 70 | 266,640 | 260.4 | 20.67% |
| 60 | 239,376 | 233.8 | 28.78% |
| **50，用户同意并已采用** | **215,088** | **210.0** | **36.01%** |
| 40 | 190,832 | 186.4 | 43.23% |
| 30 | 163,382 | 159.6 | 51.39% |
| 20 | 137,494 | 134.3 | 59.09% |

已查看 200px／300px 显示下的标题、标志、两张人物图与通用舞者图，以及较低分的七河みこ、わた图和 q20 的标题渐变。q50 在常见卡片大小下仍较接近基准，放大后会损失头发、衣服纹理及渐变细节；属于有损候选。q40 相比 q50 只再省 24,256 字节（约 23.7 KiB），更低档位的小字边缘、面部及背景块状变化逐步更明显。结合用户已接受的尺寸与体积偏好，建议将 **320 × 240 q50，约 0.21 MiB** 作为下一档候选，不以 SSIM 代替用户的视觉判断。

[本轮摘要](category-cover-strong-compression-20261113.json)记录各档总量、相对 q80 的前景 SSIM、低分样本及预览检查。完整逐图数据、候选与交互对照页位于忽略目录 `artifacts/category-cover-implementation/strong-compression/`。对照页在隔离 Edge 中验证 6 个候选档位、每档 88 张基准／候选图片均可解码，显示宽度切换有效，无页面脚本错误；页面可以按 200／220／300／343 CSS px 切换查看全部类别。

用户随后同意采用 320 × 240 q50，已将同一批核对过的 44 张文件替换为运行成品，来源清单、逐张哈希及精确产物登记同步更新。固定脚本从归档原图／母版重新生成，全部成品字节一致；没有再次压缩前一档成品。19 项 Edge 回归及隔离真实 Console 联调通过，详见[最终验证](../validation/webui-validation.md#2026-11-13采用-q50-类别封面)。未重建日常桌面 EXE、启动游戏、修改 hosts、提交或推送。

## 前期素材交付与尺寸选择

截图 6～12 均为 3840 × 2160。完整方图的四角先由面板背景边界检测，再按截图标签逐项核对；单图有效边长约 487～509 像素。每张图按四角映射到正方形，边缘内收 0.6 个源像素以排除面板的抗锯齿边，使用三次插值完成透视校正，再用 Lanczos 缩小。处理保留图片内容，没有生成式重绘、文字重画或内容补全；标题、鼠标和界面浮层不进入成品。

与 `20261112173704` 清单逐项对照，50 个可见分组全部映射成功。`Hide` 未在界面展示，不虚构专用封面。Saskia's Dansschool、Other Fitness、K-POP Dance Practice、THE IDOLM@STER、黑糖梅、-溜一口兮-、Short Choreography 在截图中共用同一张舞者剪影，素材只保存一份，因此成品共 44 张。

推荐版采用 384 × 384 WebP、质量 88：16 张来自同版官方图片，另 28 张为截图校正版本。全部图片合计 811,728 字节。Just Dance Solo 的 Xbox 官方同版图留白与游戏不同，单独保留为对照，推荐版沿用截图构图；已换头像的网络图片不替换游戏中的旧版本。具体网络来源与判断见下方来源表。

| 保存边长 | 44 张 WebP 总字节 | JPEG 总字节 | PNG 总字节 | 220px 显示平均 SSIM | 440px 显示平均 SSIM |
| --- | ---: | ---: | ---: | ---: | ---: |
| 256 | 481,446 | 853,505 | 3,671,455 | 0.99083 | 0.97047 |
| 384 | 811,728 | 1,560,546 | 7,094,601 | 0.99470 | 0.98465 |
| 512 | 1,125,668 | 2,267,672 | 11,195,199 | 0.99604 | 0.98977 |

WebP 使用质量 88、method 6，JPEG 使用质量 88、4:4:4，PNG 为无损优化保存；不同格式的质量参数不等价。SSIM 的参照是未压缩的校正截图或网络同款，不是游戏原始纹理的还原率。384 档在约 180～220 CSS 像素的常见卡片大小下保留较好的细节，总体积比 512 档少约 28%，且不会放大当时采用的下载文件（最小 400px）。后续原图参数检查发现部分 900px 变体来自更小图像，见下节；下载文件的像素尺寸不能证明同等原生细节。截图校正后的无损 PNG 和下载的原文件另行保留，未来可重新导出尺寸；不靠放大补造细节。

本机交付位于忽略目录 `artifacts/category-covers-20261112/`：

- `category-covers-384.zip`：44 张推荐 WebP、50 类映射、可离线打开的图库、来源与校验清单，以及 Solo 官方对照图。
- `category-covers-masters.zip`：截图校正的无损母版、采用的网络原文件、Solo 官方原文件及映射来源。
- `processed/preview.html`：可切换 256／384／512、推荐版／纯截图版及显示大小的对照预览。
- `processed/manifest.json`：每张截图的 SHA-256、四角坐标、变换矩阵、类别对应、推荐来源及成品哈希；`processed/group-map.json` 是交付的类别到图片映射。

44 张成品均完整解码、核对尺寸与哈希，50 类映射与可见清单一致，两个 ZIP 的 CRC 检查通过。已目视检查全部图片的汇总预览及网络来源对照；隔离 Edge 中两套集合、三档分辨率的全部图片均加载成功，离线图库无页面脚本错误。首次受限环境中的 Edge 启动失败，随后在普通本机权限、独立临时浏览器资料下复验通过；不将环境启动失败解释为素材错误。前期交付阶段尚未改变曲库回退行为，后续运行成品及规则以上节为准。

## 曲目封面比例与更宽的类别图

当前曲库的显示框为 **4:3**，图片使用 `object-fit: contain`，完整显示源图；`img` 声明宽高为 480 × 360。见[卡片样式](../../webui/src/styles.css)、[封面组件](../../webui/src/library-covers.tsx)。后端请求 `https://aya.kiva.moe/images/small{id}.jpg`，校验 JPEG、字节数和最大边长，没有强制图片比例，见[封面获取与校验](../../internal/console/song_covers.go)。因此，界面约定与实际服务端图像需要分别核对。

本轮从 12 个不同类别各选一首请求当前使用的小图，9 张 HTTP 200，全部解码为 **480 × 360（4:3）**；ID 3766、3211、3018 返回 404。另对 ID 3108、1169、2109、1 请求游戏日志出现的无 `small` 地址，4 张均为 **1280 × 720（16:9）**。例如 Fitness Marshall 的 1169，小图裁掉了左右的部分标题，大图保留完整的「10:35 DANCE WORKOUT」及更多背景。这个结果证明这些样本存在更宽的源图，不保证全曲库尺寸一致，也没有据此更改获取策略。

| 对象 | 本轮确认的像素尺寸 | 比例与用途 |
| --- | --- | --- |
| 当前曲库小封面 | 成功的 9 / 12 张均 480 × 360 | 4:3，与现有卡片相同 |
| 对应曲目大图 | 抽查 4 张均 1280 × 720 | 16:9，示例比小图保留更多左右内容 |
| 已交付类别封面 | 44 张均 384 × 384 | 1:1；在 4:3 框中完整显示时，左右各留框宽的 12.5% |
| Just Dance Solo 官方横版备选 | 1920 × 1080 | 16:9；可安全裁取中央 1440 × 1080，再缩为 4:3 |

### 已找到的横向版本

[Xbox 官方商品图](https://store-images.s-microsoft.com/image/apps.55877.13998847071682156.f55656eb-2f14-4b71-8f8b-0a8f77f562d2.587dc16b-6a0c-4d60-bc62-6793b1e14353)为 1920 × 1080，来自 [JD2024 商品元数据](https://displaycatalog.mp.microsoft.com/v7.0/products?bigIds=9N3X075GN7V3&market=US&languages=en-us)中的 `TitledHeroArt`。它采用与游戏 Solo 方图相同系列的竖排标题、角色和色彩，但调整了主体大小与位置，左右有更多完整背景；不能称为对原方图边缘的精确恢复。中心裁切 `(240, 0)–(1680, 1080)` 只去除两侧背景，标题和主要角色仍完整。发行商[发布公告](https://news.ubisoft.com/en-us/article/5XdmjuRsJlAZT1R6JOc76G/just-dance-2024-edition-launches-october-24)也使用同构图的 960 × 540 图片。

另外核对了 Nintendo 2250 × 1266 横排标题图，以及 Xbox 3840 × 2160 无标题角色拼图。两者属于同版游戏的不同构图；直接中心裁成 4:3 分别会切到标题或右侧角色，不作为默认的等价替换。Xbox Wire 的一个旧图链接发生重定向循环，失败留档，没有把它计入可用图片。

### 头像与标志的范围限制

对已验证的 15 张官方 YouTube 同版头像，分别去掉裁切参数，读取 `=s0` 和 `=s1600`，30 个响应全部为 HTTP 200。结果仍为方形或近方形（最多相差 1～3px）；全帧及边缘像素对照、三张近方形样本的目视检查均未发现有意义的视野增加。不能把改大图片 URL 的尺寸参数当成找回裁切前的画面。

其中 9 张 `=s0` 返回尺寸高于 900px：例如 Fol2esTz 4000 × 4000、Golfy Dance Fitness 2449 × 2449、Fitness Marshall 1475 × 1476；这些可作为后续重新导出的更高像素候选。另 6 张小于 900px，例如 SouthVibes 256 × 256、七河みこ 400 × 400、Mylee Dance 499 × 499，说明此前 900px 文件含服务器放大，不能据其宽高声称增加了真实细节。此轮没有替换既有素材包。

建议保持现有 4:3 卡片：取得合适横版的类别，逐张判断后按构图适配；仅有方形头像、文字或标志的类别，完整居中并配协调底色。方图若直接铺满 4:3，会总共裁掉原高度的 25%（居中时上下各 12.5%），可能切到手部、文字或标志；拉伸则改变比例。完整保留方图加背景只改善展示，不增加原图内容。本轮未找到整套原生 4:3 类别图，不能据 Solo 个例推广到其余所有类别。

补充证据仍在忽略目录 `artifacts/category-covers-20261112/aspect-ratio/`：`song-sample-results.json` 与 `song-original-results.json` 记录响应尺寸及失败；`wide-candidates/summary.json` 记录 15 个头像和 4 个 JD2024 宽图的官方来源、尺寸、哈希和构图边界；`category-4x3-comparison.jpg` 展示三种适配方式。另已从官方横版制作独立的 `jd2024-4x3-example.webp`（640 × 480），记录裁切坐标于 `aspect-preview.json`，并目视确认标题完整。比例调查阶段只生成独立对照，原方形成品及 ZIP 保持不变；产品后续采用 512 × 384 成品，见本文首节。

## 用户截图确认的目标

最初提供的游戏截图可辨认以下八个子类别及其方形图片。该阶段只能确认图片存在，尚不能证明原图文件名、下载地址或图集切片；后来七张高清截图补齐的映射及处理成果见上节。完整截图未复制进仓库。

| 子类别 | 截图中的图片特征 |
| --- | --- |
| Just Dance Solo | Just Dance 2024 标志，粉橙色游戏封面 |
| Just Dance Duet | DUET 字样，蓝黄配色的双人图 |
| Just Dance Trio | TRIO 字样，紫底三人图 |
| Just Dance Crew | CREW 字样的方图 |
| Just Dance Fan Made | 橙底四名舞者 |
| Mash-Up | 紫底拼合舞者 |
| Kids | 蓝色怪兽 |
| Disney Party | 深蓝黄色 Just Dance 标志 |

## 当前清单与分类身份

本轮读取的 [Udon HTTPS 歌曲清单](https://api.udon.dance/Api/Songs/list) 返回 HTTP 200，版本 `20261112173704`，正文 2,913,000 字节，共 51 组、10,574 首歌曲，计数包含 `Hide`（9 首），不表示游戏界面存在 51 个可见类别。若排除 `Hide`，其余 50 组中 18 组名称命中已排除的旧预加载器映射、32 组未命中。顶层键为 `time/groups/tags/new`，`groups` 只有 `contents`；全体组属性并集只有 `groupName/major/songInfos`，没有类别图片字段。该结论限于本次响应，不断言服务端没有其他接口。

本地曲库的 `LibrarySong.group` 来自歌曲 `groupName`，已可用于按子类别名称匹配；父级 `major` 是另一层分类。映射应使用完整子类别名称，而不是所属大类或清单下标。现有字段见[后端曲库读取](../../internal/cacheproxy/library.go)与[前端数据类型](../../webui/src/types.ts)。

公开原文、响应头和复算结果保存在忽略目录 `artifacts/category-covers-20261112/`：`udon-list.json`、`udon-list-headers.txt`、`preloader-thumbnails-resources.go`、`preloader-coverage.json`。

## 已排除的原预加载器套图

原预加载器按 WannaDance 子类别名称匹配一套内嵌封面。本次接口全部 51 个分组中，有 18 个命中该静态映射，涉及 6,030 / 10,574 首歌曲。这个统计仅说明旧映射对当前类别名称的命中范围，不代表目标游戏原图覆盖率。图片目录及名称对应关系来自下列原项目源码。

固定源码快照为 `wzhqwq/VRCDancePreloader@42b31a657873db5592b7a3b54451164ccb02d90c`：

- 图片是[缩略图目录](https://github.com/wzhqwq/VRCDancePreloader/tree/42b31a657873db5592b7a3b54451164ccb02d90c/internal/gui/images/thumbnails)中的静态 JPEG，使用 `go:embed *.jpg` 打进程序；`groupNameToThumbnail` 显式维护类别名称与文件名的对应关系，不是从歌曲清单读取图片地址。[内嵌与映射代码](https://github.com/wzhqwq/VRCDancePreloader/blob/42b31a657873db5592b7a3b54451164ccb02d90c/internal/gui/images/thumbnails/resources.go#L18)
- Wanna 歌曲的本地图通过歌曲的 `Group` 生成 `group:` 标记；该版本 Wanna 的常规缩略图入口也直接使用分组图。[歌曲到类别图](https://github.com/wzhqwq/VRCDancePreloader/blob/42b31a657873db5592b7a3b54451164ccb02d90c/internal/third_party_api/universal.go#L14)
- 图片加载中或为空时，控件显示该歌曲的本地分组图；找不到类别映射时使用 `thumbnail-default.jpg`。[加载期间的回退](https://github.com/wzhqwq/VRCDancePreloader/blob/42b31a657873db5592b7a3b54451164ccb02d90c/internal/gui/widgets/thumbnail.go#L70)、[未知类别的默认图](https://github.com/wzhqwq/VRCDancePreloader/blob/42b31a657873db5592b7a3b54451164ccb02d90c/internal/gui/images/thumbnails/resources.go#L95)
- 首次加入图片的公开提交是 [`517ba767`（2025-08-07）](https://github.com/wzhqwq/VRCDancePreloader/commit/517ba767845805391c60e6168a1ae78576fa8622)。提交及当前 README 没有说明图片取自游戏、原图下载站或其他素材源。仓库根 [LICENSE](https://github.com/wzhqwq/VRCDancePreloader/blob/42b31a657873db5592b7a3b54451164ccb02d90c/LICENSE) 为 MIT，缩略图目录未单列图片来源说明。

旧套图已通过用户截图排除。以下文件及对应关系只用于追溯此前线索，不可据此实施本次游戏类别图需求。

### 原静态映射命中的 18 个类别

下表使用本次 Udon 的完整 `groupName`，文件均位于原项目 `internal/gui/images/thumbnails/`。对应关系来自[固定版本映射表](https://github.com/wzhqwq/VRCDancePreloader/blob/42b31a657873db5592b7a3b54451164ccb02d90c/internal/gui/images/thumbnails/resources.go#L21)；歌曲数按本轮清单复算。

| 子类别 | 图片文件 | 本次歌曲数 |
| --- | --- | ---: |
| Just Dance Solo | `thumbnail-justdance-solo.jpg` | 866 |
| Just Dance Duet | `thumbnail-justdance-duet.jpg` | 267 |
| Just Dance Trio | `thumbnail-justdance-trio.jpg` | 137 |
| Just Dance Crew | `thumbnail-justdance-crew.jpg` | 117 |
| Just Dance Fan Made | `thumbnail-justdance-fan.jpg` | 317 |
| FitDance | `thumbnail-fitdance.jpg` | 530 |
| Fitness Marshall | `thumbnail-marshall.jpg` | 396 |
| Golfy Dance Fitness | `thumbnail-golfy.jpg` | 1022 |
| Mylee Dance | `thumbnail-mylee.jpg` | 205 |
| Song^_^ | `thumbnail-song.jpg` | 186 |
| SouthVibes | `thumbnail-southvibes.jpg` | 299 |
| TML Crew | `thumbnail-tml.jpg` | 599 |
| Fol2esTz | `thumbnail-fol2estz.jpg` | 164 |
| Lisa Rhee | `thumbnail-lisa.jpg` | 302 |
| 足太ぺんた | `thumbnail-penta.jpg` | 310 |
| Michael Jackson | `thumbnail-michael.jpg` | 32 |
| Other Fitness | `thumbnail-fitness.jpg` | 76 |
| Other K-POP | `thumbnail-kpop.jpg` | 205 |

目录读取确认这些文件存在；本轮只下载并解码核对了以下四张原图，没有逐张验证其余图片内容：

| 样本文件 | 尺寸 | 文件字节数 |
| --- | --- | ---: |
| `thumbnail-justdance-solo.jpg` | 320 × 180 | 41,454 |
| `thumbnail-song.jpg` | 320 × 180 | 31,017 |
| `thumbnail-marshall.jpg` | 320 × 180 | 31,192 |
| `thumbnail-penta.jpg` | 320 × 180 | 34,208 |

样本通过 GitHub connector 以 base64 读取，原样还原 JPEG，保存在上述忽略目录；未编辑图片。旧 Just Dance Solo 图有 Just Dance 标志与“单人”字样，Song^_^ 与足太ぺんた图是带类别文字的头像图。旧 Solo 图与用户截图中的 Just Dance 2024 方图明显不同。

### 当前清单未命中原映射的 33 类

以下名称在本次清单存在，但不在原预加载器映射表内，因此按该程序现有逻辑会使用通用图；这不表示游戏内没有对应封面。

- Hide
- Sunny Funny Fitness
- Ellen & Brian
- 초영
- 七河みこ
- まなこ
- わた
- 팔성
- VRChat Dance
- Mash-Up
- Kids
- Disney Party
- Hip Hop Dance Experience
- Kkardio Dance
- AJDanceFit
- UPVOTE Entertainment
- めろちん
- 椭奇
- Smurfs Dance Party
- ぽるし
- WAWA DANCE
- 刘逗逗不怎么逗
- DJ Raphi
- Mr彦Alex
- THE IDOLM@STER
- ABBA
- Saskia's Dansschool
- K-POP Dance Practice
- 黑糖梅
- -溜一口兮-
- Short Choreography
- Other
- Other ACGN Dance

## 用户提供的游戏日志调查

随后只读检查了用户提供的 200 份 VRC 文本日志，共 373,320,722 字节、2,221,154 行，文件覆盖 2026-05-17 至 2026-11-12。这些日志不是完整网络捕获，不能保证包含全部图片加载记录。原始日志未复制到公开仓库；以下仅保留图片域名、路径规则与脱敏的组件统计。

全量 URL 提取确认 `aya.kiva.moe` 出现 45,517 次，归一化后为 1,993 个独立路径，全部符合 `https://aya.kiva.moe/images/{数字}.jpg`。其中 1,992 个数字 ID 命中本次 Udon 歌曲清单；余下 `5274` 是[此前对照已确认的 WannaInfo 独有旧歌曲](catalog-sources-20261029.md#本轮样本内容比较)。这些记录定位的是按歌曲 ID 寻址的图片源，没有出现分类名、负数 ID、`small` 前缀或图集路径。这里的“出现次数”是日志中的 URL 文本次数，不是独立网络请求数或下载成功数。

图片相关组件主要为 `VideoImageManager`、`VideoImageHandler` 和 SDK 的 `Image Download`。日志中可见 `Image cached for danceId=…`、`DownloadImageForDanceIds called with danceIds: …`、`OnImageLoadSuccess` 等消息；`VideoImageManager` 的缓存消息共 4,547 条，记录 ID 均为正数。`VideoImageHandler` 的成功消息中有 16 条 `danceId=0`，抽查前 6 条均指向另一房间的 `api.dudufit.dance/thumbnails/0.jpg`，不能据此认定 WannaDance 使用 0 号类别图。队列 JSON 中的 `groupName` 只提供歌曲所属分组，没有建立分类与图片的对应关系。

对 `defaultThumbnail/defaultSprite/defaultTexture/defaultImage/groupImage/categoryImage` 等字段名，以及分类词与 `image/thumbnail/sprite/texture/atlas` 的相邻记录检索，未找到可用映射。观察到的 atlas/sprite 提示涉及字体图集、Avatar 名称、StyleEngine 资源或缺失精灵，不能作为点歌类别封面的证据。全量提取到的 `api.udon.dance` 路径为歌曲 `play/list`，没有分类图片接口记录。

这一批日志能确认游戏使用的歌曲图片地址，但仍不能定位全部子类别封面的素材与映射。类别图预置在世界资源中、或其加载没有写入这些日志，都是可能解释，尚未得到证实；缺少 URL 记录不表示类别图不存在。

去除私人字段后的复算摘要为忽略目录 `artifacts/category-covers-20261112/vrc-log-analysis/wanna-image-log-summary.json`；详细 URL 索引与组件分析的 `*-private.json` 仅保留在本地忽略目录。原始玩家、房间、令牌及签名参数不写入本报告。

## 缓存只读检查与工具限制

本机缓存目录共有 14 个 `__data` 文件，文件头已全部只读核对，均为 `UnityFS`、版本 `6000.0.67f1-DWR`。使用 UnityPy 1.25.4 读取修改时间最新的三个样本时，库均报告 bundle 加密且未提供 key，尚未从这三个样本解析到资源对象、贴图或类别绑定。另有一份 Avatar 类型样本可解析，但没有取得目标类别图，因此并非所有样本都无法解析。本轮没有获取密钥或尝试绕过加密。文件头与读取结果保存在本地忽略证据 `artifacts/category-covers-20261112/unity-cache-research/cache-header-summary-private.json`，不列私人缓存路径、缓存标识或无关资产细节。

这三个样本尚未绑定到目标 WannaDance 世界。公开世界元数据可匿名读取，但本轮返回的 `unityPackages` 为空，未取得其 bundle 的 `file_ID`。公开 VRCX 实现使用 `file_ID` 与 variant 生成缓存目录标识，而不是直接使用 `wrld_ID`，因此不能仅凭目标世界 ID 确认某个缓存文件的归属。[目录标识生成](https://github.com/vrcx-team/VRCX/blob/2929696b3d43389b30d5d9c234b8fcc26932ba22/Dotnet/AssetBundleManager.cs#L23)、[世界素材 URL 到文件 ID 的调用链](https://github.com/vrcx-team/VRCX/blob/2929696b3d43389b30d5d9c234b8fcc26932ba22/src/coordinators/cacheCoordinator.js#L42)

本轮缓存结果只能说明通用读取工具未能解析这三个样本，不能写成已经识别或提取了 WannaDance 世界缓存。私人缓存路径及世界 instance 不写入本报告。

## 此前工程与缓存调查的边界

已查看曲库网页所属组织的公开 [AyaDance Unity 仓库](https://github.com/ClownpieceStripedAbyss/aya-dance-unity/tree/c496901cdc9c47952fd68ee8d1088c5a3e556fb9)，没有找到能把 WannaDance 上述 51 个子类别逐项关联到封面的业务代码或资产表；该仓库不能据此视为当前 WannaDance 世界工程。另一个同名 `jackutea/wannadance` 是键盘音游项目，与本次目标无关。

上述调查阶段没有取得游戏工程、原始图集或类别绑定表，用户当时也没有现成素材来源。后续截图已提供可见类别与图片的对应关系，并结合下方网络来源完成素材交付；这不代表缓存提取成功。旧预加载器套图及构图、版本不同的泛用图片仍未作为目标素材替代。

## 网络同版高清来源

后续用户提供七张高清截图后，按具体图像逐项查找公开来源。已下载并目视核对 17 类同版图像；再经截图与网络图并排查看及 SIFT 辅助核对，其中 16 类构图基本一致，可直接采用对应官方图片。Just Dance Solo 的 Xbox 方形封面属于同一版艺术，但纵向留白与位置相对 500px 校正版约有 34px 差异，推荐继续使用截图版，Xbox 图仅另附对照。此处记录候选与采用判断，不表示产品已经接入。

| 类别 | 实际下载尺寸 | 一手来源 | 采用判断 |
| --- | --- | --- | --- |
| Just Dance Solo | 2160 × 2160 | [Xbox 官方商店](https://www.xbox.com/en-us/games/store/just-dance-2024-edition/9N3X075GN7V3) | 仅供对照，推荐截图版 |
| FitDance | 400 × 400 | [FitDance 官方 X](https://x.com/FitDanceOficial) | 可直接采用 |
| Fitness Marshall | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/@TheFitnessMarshall) | 可直接采用 |
| Golfy Dance Fitness | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/@GolfyDanceFitness) | 可直接采用 |
| Mylee Dance | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/c/MYLEEDanceTV) | 可直接采用 |
| SouthVibes | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/channel/UCgF_CL32PraVYKY_sw7oaNA) | 可直接采用 |
| Sunny Funny Fitness | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/channel/UCTAcO7MyXetuvExV7Lo_L6Q) | 可直接采用 |
| TML Crew | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/channel/UCmzRlnYu2x5l-kmjpF08Hnw) | 可直接采用 |
| Kkardio Dance | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/channel/UC90qhppagynYF6wbqo6I9LQ) | 可直接采用 |
| Ellen & Brian | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/@EllenandBrian) | 可直接采用 |
| Fol2esTz | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/channel/UCzogBN1kUo909dyrnLHFyyQ) | 可直接采用 |
| 초영 | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/channel/UCeU2BHh5BQVMRfc6-ZRXgNw) | 可直接采用 |
| UPVOTE Entertainment | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/@upvoteentertainment) | 可直接采用 |
| WAWA DANCE | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/channel/UC0fwn6TtSHwnnrd01yPZmAg) | 可直接采用 |
| 七河みこ | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/channel/UCmlFEXJy7_aGySlI4P5C-Rg) | 可直接采用 |
| わた | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/channel/UCLef73Thh-b-OW6ubcbN5mw) | 可直接采用 |
| めろちん | 900 × 900 | [创作者官方 YouTube](https://www.youtube.com/channel/UCMcWosflbve0JX78FFdJcCA) | 可直接采用 |

原始候选及其来源 URL、文件尺寸、字节数、SHA-256、逐项比对与采用判断保存在忽略目录 `artifacts/category-covers-20261112/web-candidates/verified-candidates.json`。其中 `asset` 对齐截图提取清单：`melochin → merochin`、`ellen-and-brian → ellen-brian`、`kkardio-official → kkardio-dance`、`fitness-marshall-official → fitness-marshall`，文件名别名另有记录。

DJ Raphi、AJDanceFit、Lisa Rhee、足太ぺんた、まなこ、팔성、ぽるし的当前官方头像或 Logo 均已变更，与游戏截图不同，已明确拒绝替换。Nintendo 横版 Just Dance 2024 宣传图与 Xbox 无标题角色拼图也因构图不同而拒绝；Fitness Marshall 的转载图、Kkardio 活动页图和 IGDB 竖版封面仅保留为溯源及对照，未把它们写成官方原图。
