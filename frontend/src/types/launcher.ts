export type LoaderType = 'fabric' | 'forge' | 'neoforge' | 'quilt' | 'optifine' | 'vanilla';

export interface InstalledLoader {
  type: LoaderType;
  version: string;
  isPrimary: boolean;
  updateAvailable?: string;
  installDate?: string;
}

export interface ResourcePackItem {
  id: string;
  name: string;
  fileName: string;
  description: string;
  resolution: '16x' | '32x' | '64x' | '128x';
  enabled: boolean;
  priority: number;
  icon: string;
  fileSizeBytes: number;
}

export interface ShaderPackItem {
  id: string;
  name: string;
  fileName: string;
  author: string;
  description: string;
  enabled: boolean;
  profile: 'Low' | 'Medium' | 'High' | 'Ultra';
  fileSizeBytes: number;
}

export interface Instance {
  id: string;
  name: string;
  mcVersion: string;
  loader: LoaderType;
  loaderVersion: string;
  installedLoaders?: InstalledLoader[];
  partition: string; // 实例分区 / 分组, 例如: "大型整合包", "纯净原版", "生电测试", "怀旧服"
  icon: string;
  bannerUrl?: string;
  playTimeMinutes: number;
  lastPlayed: string;
  javaVersion: string;
  customJavaPath?: string; // 允许实例独立指定使用的本地 javaruntime
  memoryMb: number;
  jvmArgs: string;
  modsCount: number;
  description: string;
  isFavorite: boolean;
  status: 'ready' | 'launching' | 'running';
  folderPath: string; // 格式: instances/<instance_name>/.minecraft/
  isolatedDirectory: boolean; // 独立游戏核心目录，与其它实例互不影响
}

/**
 * 展示层使用的 Java 运行时信息。
 *
 * 字段来源有两个：设计稿的演示数据，以及后端 `RuntimeService` 的真实探测结果。
 * 真实结果不含绝对路径（后端有意不下发路径），也不含安装体积，因此路径与体积
 * 三项都是可选的——界面在缺失时显示「—」，不得用编造的路径或大小填充。
 */
export interface JavaRuntime {
  id: string;
  /** 主版本号。真实探测到的可能是 8、11、16 等任意值，不限于设计稿的四档。 */
  majorVersion: number;
  vendor: string; // e.g. Eclipse Temurin, GraalVM CE, Microsoft OpenJDK, Oracle
  versionString: string; // e.g. 21.0.3+9
  /** CPU 架构；后端可能给出 x86、aarch64 或未知。 */
  architecture: string;
  /** 安装目录（仅演示数据有；真实数据为空）。 */
  folderPath?: string;
  /** 可执行文件路径（仅演示数据有；后端不下发路径）。 */
  binaryPath?: string;
  isDownloadedByLauncher: boolean;
  /** 安装体积（MB）；后端不下发时为 undefined。 */
  sizeMb?: number;
  status: 'ready' | 'downloading' | 'error';
  downloadProgress?: number;
  /** 与本机架构不兼容的原因（兼容时为空）。 */
  incompatibleReason?: string;
}

export interface Account {
  id: string;
  username: string;
  uuid: string;
  type: 'microsoft' | 'offline' | 'third_party';
  authServerName?: string; // e.g. LittleSkin, 自建 Yggdrasil, Blessing Skin
  authServerUrl?: string; // e.g. https://littleskin.cn/api/yggdrasil
  email?: string;
  skinUrl: string;
  capeUrl?: string;
  capeName?: string;
  active: boolean;
  createdDate: string;
  tokenExpiry?: string;
}

export interface ModItem {
  id: string;
  name: string;
  fileName: string;
  slug: string;
  author: string;
  summary: string;
  downloads: string;
  follows: string;
  category: 'performance' | 'tech' | 'magic' | 'adventure' | 'shaders' | 'resourcepack' | 'modpack';
  loaders: LoaderType[];
  gameVersions: string[];
  iconUrl: string;
  installed: boolean;
  enabled: boolean;
  installedVersion?: string;
  latestVersion: string;
  fileSizeBytes: number;
  source: 'modrinth' | 'curseforge';
  updateAvailable?: boolean;
}

export interface DownloadTask {
  id: string;
  name: string;
  category: 'core' | 'mod' | 'assets' | 'java' | 'loader' | 'shader' | 'resourcepack';
  progress: number; // 0 - 100
  downloadedBytes: number;
  totalBytes: number;
  speed: string; // e.g. "8.4 MB/s"
  status: 'downloading' | 'completed' | 'paused' | 'failed';
  targetFolder?: string;
  /** 后端原始状态（九态之一），保留用于展示排队/校验/提交等细节。 */
  rawState?: string;
  /** 已完成制品数（父任务为已完成子任务数）。 */
  completedCount?: number;
  /** 制品总数。 */
  artifactCount?: number;
  /** 失败原因（仅失败时出现）。 */
  errorMessage?: string;
}

export interface LogEntry {
  id: string;
  timestamp: string;
  level: 'INFO' | 'WARN' | 'ERROR' | 'FATAL' | 'DEBUG';
  source: string;
  message: string;
}

export interface CrashDiagnosis {
  title: string;
  cause: string;
  affectedComponents: string[];
  recommendation: string;
  oneClickActionLabel?: string;
  actionType?: 'increase_ram' | 'update_loader' | 'remove_mod' | 'switch_java';
}

export interface LauncherSettings {
  downloadSource: 'official' | 'bmclapi';
  javaPath: string;
  maxMemoryMb: number;
  minMemoryMb: number;
  autoDownloadJava: boolean;
  instancesDirectory: string; // 默认 instances/ 根目录
  javaRuntimeDirectory: string; // 默认 javaruntime/ 根目录
  theme: 'dark' | 'light';
  accentTheme: 'emerald' | 'cyan' | 'amethyst' | 'redstone' | 'amber';
  fullscreen: boolean;
  resolutionWidth: number;
  resolutionHeight: number;
  jvmPreset: 'standard' | 'aikar' | 'zgc' | 'shenandoah';
  enableDiscordRPC: boolean;
  closeOnLaunch: boolean;
}
