// Package artifact 定义 NineVoxel Launcher 的制品模型与「逻辑目标 → 本地路径」规则。
//
// # 职责边界
//
// 本包只做三件事：
//  1. 描述制品（Artifact / ArtifactSource / ArtifactReference / InstallRecord）；
//  2. 把逻辑目标解析成**相对数据根**的确定路径（ResolveTarget、ObjectRelPath 等）；
//  3. 给出共享策略判定（CanShare / IsVerifiable / TrustLevel / SortArtifacts）。
//
// 本包**不**下载、**不**写业务文件、**不**创建目录、**不**读时钟、**不**用随机数。
// 真正的目录创建与文件写入由 P3-06 在下载事务内完成；本包只定义「应该落在哪里」。
// 唯一需要读取运行环境的函数是 DataRoot，它把判定逻辑抽成纯函数 ResolveDataRoot 以便测试。
//
// # 路径规则表（生产代码实际使用的落盘位置）
//
// 相对路径一律用正斜杠（slash）形式表示，拼接成绝对路径前用 filepath.FromSlash 转换。
// 下表以运行时**实际写入**的调用链为准（2026-11 核对）：资产对象与索引只落 assets/，
// 依赖库直达 Maven 布局，artifacts/objects 仅承载加载器官方安装器。
//
//	| kind                        | 实际落点                                       | 生成函数               |
//	| --------------------------- | ---------------------------------------------- | ---------------------- |
//	| client                      | versions/<versionId>/<versionId>.jar           | ResolveTarget           |
//	| library                     | libraries/<maven path>                         | LibraryRelPath          |
//	| asset                       | assets/objects/<hash 前 2 位>/<hash>           | AssetObjectRelPath      |
//	| asset index / log config    | assets/indexes|log_configs/...                 | AssetIndex/LogConfig    |
//	| runtime                     | runtimes/<runtimeId>/...                       | RuntimeRelPath          |
//	| 加载器官方安装器            | artifacts/objects/<sha1 前 2 位>/<sha1>        | ObjectRelPath           |
//	| native（解压产物）           | instances/<id>/.minecraft/natives/             | 核心清单外，不属于本包 |
//	| mod / resource_pack / ...   | 无（P3 拒绝）                                  | 实例级内容（D-29）      |
//
// ResolveTarget 对 library/native/asset 的 `artifacts/objects` 映射是兼容分支，
// 生产代码不再使用；新增调用点必须走上面列出的专用函数。
//
// 非制品类的固定路径（同样相对数据根）：
//
//	| 用途            | 相对路径形态                                        |
//	| --------------- | --------------------------------------------------- |
//	| 资产索引        | assets/indexes/<indexId>.json                       |
//	| 日志配置        | assets/log_configs/<name>                           |
//	| 元数据缓存      | artifacts/metadata/<fileName>（当前无调用）         |
//	| 下载临时文件    | downloads/staging/<taskId>.part                    |
//	| 损坏隔离        | downloads/quarantine/<hash 前 2 位>/<taskId>.bad   |
//	| 日志            | logs/                                              |
//	| 数据库与备份    | db/app.db、db/app.db.backup-*                      |
//
// # 内容寻址
//
// 资产对象路径固定为 assets/objects/<hash 前 2 位>/<hash>；加载器安装器沿用
// artifacts/objects/<hash 前 2 位>/<hash>。
// 资产（asset）以 SHA-1 作寻址键（决策 D-27：assetIndex 只提供 SHA-1，
// 自行计算 SHA-256 会把同一资产分裂成两份），因此 ObjectRelPath 同时接受
// sha1 与 sha256 两种算法，但**要求调用方显式声明**算法，严禁用 SHA-1 值冒充
// SHA-256 键。SHA-512 只作为附加校验信息记录在 Hashes 里，不参与寻址。
//
// # 与 P2 路径校验的语义对照
//
// P2 的 resolveInside 是 internal/launch 包私有函数，跨包不可见（P3-03 卡面第 7 条）。
// 本包的 ValidateRelativePath / ResolveInsideRoot / EnsureWithinRoot 与它逐条对齐：
//
//	| 攻击面           | P2 resolveInside              | 本包实现                                   |
//	| ---------------- | ----------------------------- | ------------------------------------------ |
//	| 空相对路径       | 合法，返回 root 本身          | ResolveInsideRoot 同；ResolveTarget 视为非法 |
//	| NUL 字符         | 拒绝                          | 拒绝                                       |
//	| 绝对路径 / 前导分隔符 | 拒绝                    | 拒绝                                       |
//	| 盘符切换（C:foo）| 拒绝                          | 拒绝                                       |
//	| `..` 段          | 逐段拒绝                      | 逐段拒绝                                   |
//	| 前缀边界         | filepath.Rel，非字符串前缀    | 相同                                       |
//	| 符号链接逃逸     | EvalSymlinks 后复检           | 目标存在时相同；不存在时校验父目录链        |
//
// # 遗留问题
//
// Windows 长路径（超过 260 字符）**未做专门处理**：本包不对路径加 `\\?\` 前缀，
// 也不检测长度。候选方案为统一加 `\\?\` 前缀或要求用户启用系统长路径组策略，
// 需要真实长路径环境验证，留待后续阶段评估（P3-03 卡面遗留第 4 条，Leader 已决定 P3 不处理）。
package artifact
