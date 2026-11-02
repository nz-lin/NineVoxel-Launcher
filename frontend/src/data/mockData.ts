/**
 * 演示数据。
 *
 * 三张横幅图通过 import 引入，不能写成 '/src/assets/...' 字符串——
 * 字符串路径在开发服务器下能命中，但生产构建时 Vite 不会把它当模块处理，
 * 构建产物里没有这个文件，界面会显示空白。
 */
import ninevoxelHero from '../assets/images/ninevoxel_hero_bg_1793533498523.jpg';
import modpackCreateFactory from '../assets/images/modpack_create_factory_1793533498690.jpg';
import modpackEnchantedRealm from '../assets/images/modpack_enchanted_realm_1793533499129.jpg';
import {
  Instance,
  Account,
  ModItem,
  LauncherSettings,
  LogEntry,
  JavaRuntime,
  ShaderPackItem,
  ResourcePackItem
} from '../types/launcher';

export const INITIAL_INSTANCES: Instance[] = [
  {
    id: 'inst-1',
    name: '1.21.4 原版极速优化 (Sodium + Iris)',
    mcVersion: '1.21.4',
    loader: 'fabric',
    loaderVersion: '0.16.10',
    installedLoaders: [
      { type: 'fabric', version: '0.16.10', isPrimary: true, updateAvailable: '0.16.14', installDate: '2025-01-10' }
    ],
    partition: '常用模组包',
    icon: '⚡',
    bannerUrl: ninevoxelHero,
    playTimeMinutes: 1420,
    lastPlayed: '今天 10:24',
    javaVersion: 'Java 21 (GraalVM CE)',
    customJavaPath: 'javaruntime/java-21/bin/java',
    memoryMb: 6144,
    jvmArgs: '-XX:+UseG1GC -XX:+UnlockExperimentalVMOptions -XX:G1NewSizePercent=20',
    modsCount: 18,
    description: '采用 Fabric 平台搭建的轻量级纯优化配置，集成 Sodium 渲染加速与 Iris 光影支持，帧率提升 300% 以上。',
    isFavorite: true,
    status: 'ready',
    folderPath: 'instances/1.21.4-Fabric-Opt/.minecraft',
    isolatedDirectory: true
  },
  {
    id: 'inst-2',
    name: '机械纪元：自动化工业 1.20.1',
    mcVersion: '1.20.1',
    loader: 'forge',
    loaderVersion: '47.3.0',
    installedLoaders: [
      { type: 'forge', version: '47.3.0', isPrimary: true, updateAvailable: '47.3.12', installDate: '2024-11-20' },
      { type: 'optifine', version: 'HD_U_I6', isPrimary: false }
    ],
    partition: '大型科技服',
    icon: '⚙️',
    bannerUrl: modpackCreateFactory,
    playTimeMinutes: 3840,
    lastPlayed: '昨天 21:50',
    javaVersion: 'Java 17 (Eclipse Temurin)',
    customJavaPath: 'javaruntime/java-17/bin/java',
    memoryMb: 8192,
    jvmArgs: '-XX:+UseZGC -XX:+ZGenerational -XX:+UnlockExperimentalVMOptions',
    modsCount: 64,
    description: '基于 Create 机械动力与工业自动化的中型科技整合包，包含动力传动、自动化流水线与火车物流网络。',
    isFavorite: true,
    status: 'ready',
    folderPath: 'instances/Create-Industry-1.20.1/.minecraft',
    isolatedDirectory: true
  },
  {
    id: 'inst-3',
    name: '幻境异界：魔法与神话 1.21.1',
    mcVersion: '1.21.1',
    loader: 'neoforge',
    loaderVersion: '21.1.65',
    installedLoaders: [
      { type: 'neoforge', version: '21.1.65', isPrimary: true, updateAvailable: '21.1.80', installDate: '2025-02-01' }
    ],
    partition: '大型科技服',
    icon: '🔮',
    bannerUrl: modpackEnchantedRealm,
    playTimeMinutes: 620,
    lastPlayed: '3 天前',
    javaVersion: 'Java 21 (Microsoft OpenJDK)',
    customJavaPath: 'javaruntime/java-21/bin/java',
    memoryMb: 8192,
    jvmArgs: '-XX:+UseG1GC -XX:MaxGCPauseMillis=50',
    modsCount: 42,
    description: 'NeoForge 新生代模组生态，包含神秘遗迹探索、全新魔法符文系统与华丽的粒子法术效果。',
    isFavorite: false,
    status: 'ready',
    folderPath: 'instances/Magic-Realm-1.21.1/.minecraft',
    isolatedDirectory: true
  },
  {
    id: 'inst-4',
    name: '纯净原版最新正式版 1.21.4',
    mcVersion: '1.21.4',
    loader: 'vanilla',
    loaderVersion: 'Release',
    installedLoaders: [],
    partition: '纯净与测试',
    icon: '🟩',
    playTimeMinutes: 180,
    lastPlayed: '上周',
    javaVersion: 'Java 21 (Auto Match)',
    customJavaPath: 'javaruntime/java-21/bin/java',
    memoryMb: 4096,
    jvmArgs: '-XX:+UseG1GC',
    modsCount: 0,
    description: 'Mojang 官方官方无修改发布版，体验试炼密室、旋风人与重锤战斗等原生特性。',
    isFavorite: false,
    status: 'ready',
    folderPath: 'instances/1.21.4-Vanilla/.minecraft',
    isolatedDirectory: true
  },
  {
    id: 'inst-5',
    name: '远古怀旧服 1.7.10 虚无世界',
    mcVersion: '1.7.10',
    loader: 'forge',
    loaderVersion: '10.13.4.1614',
    installedLoaders: [
      { type: 'forge', version: '10.13.4.1614', isPrimary: true, installDate: '2023-08-15' }
    ],
    partition: '怀旧经典',
    icon: '🗡️',
    playTimeMinutes: 5240,
    lastPlayed: '2 周前',
    javaVersion: 'Java 8 (Oracle OpenJDK)',
    customJavaPath: 'javaruntime/java-8/bin/java',
    memoryMb: 4096,
    jvmArgs: '-XX:+UseG1GC',
    modsCount: 29,
    description: '经典模组黄金时代，包含虚无世界2与拔刀剑，重温热血生存回忆。',
    isFavorite: false,
    status: 'ready',
    folderPath: 'instances/1.7.10-Nevermine/.minecraft',
    isolatedDirectory: true
  }
];

export const INITIAL_JAVA_RUNTIMES: JavaRuntime[] = [
  {
    id: 'jre-21',
    majorVersion: 21,
    vendor: 'GraalVM CE (Adoptium)',
    versionString: '21.0.3+9-LTS',
    architecture: 'x64',
    folderPath: 'Ninevoxel/javaruntime/java-21/',
    binaryPath: 'Ninevoxel/javaruntime/java-21/bin/java',
    isDownloadedByLauncher: true,
    sizeMb: 245,
    status: 'ready'
  },
  {
    id: 'jre-17',
    majorVersion: 17,
    vendor: 'Eclipse Temurin OpenJDK',
    versionString: '17.0.11+9-LTS',
    architecture: 'x64',
    folderPath: 'Ninevoxel/javaruntime/java-17/',
    binaryPath: 'Ninevoxel/javaruntime/java-17/bin/java',
    isDownloadedByLauncher: true,
    sizeMb: 198,
    status: 'ready'
  },
  {
    id: 'jre-8',
    majorVersion: 8,
    vendor: 'Eclipse Temurin OpenJDK 8u',
    versionString: '8u412-b08',
    architecture: 'x64',
    folderPath: 'Ninevoxel/javaruntime/java-8/',
    binaryPath: 'Ninevoxel/javaruntime/java-8/bin/java',
    isDownloadedByLauncher: true,
    sizeMb: 112,
    status: 'ready'
  },
  {
    id: 'jre-25',
    majorVersion: 25,
    vendor: 'OpenJDK Early-Access',
    versionString: '25-ea+10',
    architecture: 'x64',
    folderPath: 'Ninevoxel/javaruntime/java-25/',
    binaryPath: 'Ninevoxel/javaruntime/java-25/bin/java',
    isDownloadedByLauncher: false,
    sizeMb: 260,
    status: 'ready'
  }
];

export const INITIAL_ACCOUNTS: Account[] = [
  {
    id: 'acc-1',
    username: 'Alex_Ninevoxel',
    uuid: '9e38f1b2-c0de-4a87-9921-88f12a3d09a1',
    type: 'microsoft',
    email: 'alex_voxel@outlook.com',
    skinUrl: 'https://images.unsplash.com/photo-1566492031773-4f4e44671857?w=120&auto=format&fit=crop&q=80',
    capeUrl: 'https://minecraft.wiki/images/Mojang_Cape.png',
    capeName: '15th Anniversary Cape',
    active: true,
    createdDate: '2023-04-12',
    tokenExpiry: '2026-11-20'
  },
  {
    id: 'acc-2',
    username: 'Steve_Architect',
    uuid: 'e499d63c-31a8-4221-8ecb-36528751e39a',
    type: 'offline',
    skinUrl: '',
    capeUrl: '',
    active: false,
    createdDate: '2024-01-08'
  },
  {
    id: 'acc-3',
    username: 'Kitsune_Craft',
    uuid: '71c89f2a-714e-4f01-bcae-2139886a1102',
    type: 'third_party',
    authServerName: 'LittleSkin 皮肤站',
    authServerUrl: 'https://littleskin.cn/api/yggdrasil',
    email: 'kitsune@foxmail.com',
    skinUrl: '',
    capeName: 'Custom Voxel Cape',
    active: false,
    createdDate: '2024-06-15'
  }
];

export const MOCK_MODS: ModItem[] = [
  {
    id: 'mod-sodium',
    name: 'Sodium (钠)',
    fileName: 'sodium-fabric-0.6.0+mc1.21.4.jar',
    slug: 'sodium',
    author: 'jellysquid3 / CaffeineMC',
    summary: '现代渲染优化模组，大幅提升游戏渲染管线性能，显著减少微卡顿并稳定帧数。',
    downloads: '38.4M',
    follows: '412K',
    category: 'performance',
    loaders: ['fabric', 'neoforge'],
    gameVersions: ['1.21.4', '1.21.1', '1.20.1'],
    iconUrl: '⚡',
    installed: true,
    enabled: true,
    installedVersion: '0.6.0',
    latestVersion: '0.6.2',
    updateAvailable: true,
    fileSizeBytes: 2450000,
    source: 'modrinth'
  },
  {
    id: 'mod-iris',
    name: 'Iris Shaders (虹)',
    fileName: 'iris-mc1.21.4-1.8.0.jar',
    slug: 'iris',
    author: 'coderbot',
    summary: '与 Sodium 完美兼容的高性能光影模组，无需 OptiFine 即可加载主流光影包。',
    downloads: '29.1M',
    follows: '320K',
    category: 'shaders',
    loaders: ['fabric', 'neoforge'],
    gameVersions: ['1.21.4', '1.21.1', '1.20.1'],
    iconUrl: '🌈',
    installed: true,
    enabled: true,
    installedVersion: '1.8.0',
    latestVersion: '1.8.0',
    updateAvailable: false,
    fileSizeBytes: 4120000,
    source: 'modrinth'
  },
  {
    id: 'mod-create',
    name: 'Create (机械动力)',
    fileName: 'create-1.20.1-0.5.1.f.jar',
    slug: 'create',
    author: 'simibubi',
    summary: '革命性的机械建筑与自动化模组，使用转轴、齿轮、皮带与传动轴搭建富有美感的动态装置。',
    downloads: '45.7M',
    follows: '560K',
    category: 'tech',
    loaders: ['forge', 'fabric', 'neoforge'],
    gameVersions: ['1.20.1', '1.19.2', '1.18.2'],
    iconUrl: '⚙️',
    installed: true,
    enabled: true,
    installedVersion: '0.5.1f',
    latestVersion: '0.5.1j',
    updateAvailable: true,
    fileSizeBytes: 22800000,
    source: 'curseforge'
  },
  {
    id: 'mod-lithium',
    name: 'Lithium (锂)',
    fileName: 'lithium-fabric-0.14.3.jar',
    slug: 'lithium',
    author: 'CaffeineMC',
    summary: '通用物理与服务端逻辑优化模组，显著改善实体 AI、区块加载与红石运算的 Tick 延迟。',
    downloads: '26.8M',
    follows: '190K',
    category: 'performance',
    loaders: ['fabric', 'neoforge', 'quilt'],
    gameVersions: ['1.21.4', '1.21.1', '1.20.1'],
    iconUrl: '🔋',
    installed: true,
    enabled: true,
    installedVersion: '0.14.3',
    latestVersion: '0.14.3',
    fileSizeBytes: 720000,
    source: 'modrinth'
  },
  {
    id: 'mod-jei',
    name: 'Just Enough Items (JEI)',
    fileName: 'jei-1.21.4-fabric-19.8.0.75.jar',
    slug: 'jei',
    author: 'mezz',
    summary: '必备的物品配方管理器，随时查阅工作台合成、熔炼、酿造以及各种模组机器配方。',
    downloads: '240M',
    follows: '890K',
    category: 'tech',
    loaders: ['forge', 'fabric', 'neoforge'],
    gameVersions: ['1.21.4', '1.21.1', '1.20.1', '1.16.5'],
    iconUrl: '📖',
    installed: true,
    enabled: true,
    installedVersion: '19.8.0.75',
    latestVersion: '19.8.0.75',
    fileSizeBytes: 1850000,
    source: 'curseforge'
  },
  {
    id: 'mod-appleskin',
    name: 'AppleSkin (苹果皮)',
    fileName: 'AppleSkin-mc1.21-fabric-2.5.1.jar',
    slug: 'appleskin',
    author: 'squeek502',
    summary: '食物信息可视化模组，直接在物品栏显示饥饿值恢复量与隐藏的饱和度数值。',
    downloads: '92M',
    follows: '310K',
    category: 'performance',
    loaders: ['fabric', 'forge', 'neoforge', 'quilt'],
    gameVersions: ['1.21.4', '1.21.1', '1.20.1'],
    iconUrl: '🍎',
    installed: true,
    enabled: true,
    installedVersion: '2.5.1',
    latestVersion: '2.5.1',
    fileSizeBytes: 120000,
    source: 'modrinth'
  },
  {
    id: 'mod-distant-horizons',
    name: 'Distant Horizons (遥远地平线)',
    fileName: 'DistantHorizons-2.2.1-a-1.21.4.jar',
    slug: 'distant-horizons',
    author: 'James / TheBuilderBr',
    summary: '突破 Minecraft 视距限制的超远 LOD 渲染模组，在保持极高帧率的同时呈现数千格外的山川地貌。',
    downloads: '8.9M',
    follows: '185K',
    category: 'performance',
    loaders: ['fabric', 'forge', 'neoforge'],
    gameVersions: ['1.21.4', '1.21.1', '1.20.1'],
    iconUrl: '🏔️',
    installed: false,
    enabled: false,
    latestVersion: '2.2.1-a',
    fileSizeBytes: 6900000,
    source: 'modrinth'
  },
  {
    id: 'mod-ferritecore',
    name: 'FerriteCore (铁素体核心)',
    fileName: 'ferritecore-7.0.0-fabric.jar',
    slug: 'ferritecore',
    author: 'malte0811',
    summary: '深度内存优化模组，优化模型内存占用，为大型整合包降低高达 30% - 50% 的堆内存消耗。',
    downloads: '41M',
    follows: '210K',
    category: 'performance',
    loaders: ['forge', 'fabric', 'neoforge'],
    gameVersions: ['1.21.4', '1.21.1', '1.20.1'],
    iconUrl: '🧪',
    installed: true,
    enabled: true,
    installedVersion: '7.0.0',
    latestVersion: '7.0.0',
    fileSizeBytes: 480000,
    source: 'modrinth'
  }
];

export const MOCK_SHADERS: ShaderPackItem[] = [
  {
    id: 'shader-1',
    name: 'Complementary Reimagined',
    fileName: 'ComplementaryReimagined_r5.2.2.zip',
    author: 'EminGT',
    description: '广受好评的高质量光影，兼具卓越视觉、真实物理水面与极致的性能调校。',
    enabled: true,
    profile: 'High',
    fileSizeBytes: 18400000
  },
  {
    id: 'shader-2',
    name: 'Bliss Shaders (动态体积云)',
    fileName: 'Bliss_Shaders_v2.0.4.zip',
    author: 'XorDev',
    description: 'Chocapic13 修改版，拥有业界领先的动态程序化体积云层与晨雾丁达尔光。',
    enabled: false,
    profile: 'Ultra',
    fileSizeBytes: 12100000
  },
  {
    id: 'shader-3',
    name: 'BSL Shaders',
    fileName: 'BSL_v8.2.09.zip',
    author: 'Capt Tatsu',
    description: '明亮通透的写实风格，柔和的暖色调与定制景深。',
    enabled: false,
    profile: 'Medium',
    fileSizeBytes: 15300000
  },
  {
    id: 'shader-4',
    name: 'MakeUp - Ultra Fast',
    fileName: 'MakeUp-UltraFast-8.9.zip',
    author: 'XavierFGB',
    description: '专为低配电脑优化的极速光影，保持原生体验的同时增加阴影和水体折射。',
    enabled: false,
    profile: 'Low',
    fileSizeBytes: 5400000
  }
];

export const MOCK_RESOURCEPACKS: ResourcePackItem[] = [
  {
    id: 'rp-1',
    name: 'Faithful 32x (原版高清 32 像素)',
    fileName: 'Faithful32x-1.21.4.zip',
    description: '最经典的 Minecraft 双倍分辨率高清材质包，严格忠实于原版艺术风格。',
    resolution: '32x',
    enabled: true,
    priority: 1,
    icon: '🧱',
    fileSizeBytes: 48200000
  },
  {
    id: 'rp-2',
    name: 'Fresh Animations (动作重塑)',
    fileName: 'FreshAnimations_v1.9.2.zip',
    description: '为所有生物重新制作平滑、生动的动作动画与眼部眨眼表情（需搭配 EMF 模组）。',
    resolution: '16x',
    enabled: true,
    priority: 2,
    icon: '🧟',
    fileSizeBytes: 14500000
  },
  {
    id: 'rp-3',
    name: 'Bare Bones (预告片官方极简风)',
    fileName: 'BareBones-1.21.zip',
    description: '将游戏画面还原为 Mojang 官方宣传片与动画短片中的极简干净纯色风格。',
    resolution: '16x',
    enabled: false,
    priority: 3,
    icon: '🎨',
    fileSizeBytes: 22100000
  },
  {
    id: 'rp-4',
    name: 'Stay True (自然沉浸细节)',
    fileName: 'StayTrue_1.21.zip',
    description: '重塑树叶多层次重叠、草地边缘渐变与方块无缝连接纹理，带来极致沉浸感。',
    resolution: '16x',
    enabled: false,
    priority: 4,
    icon: '🌿',
    fileSizeBytes: 34800000
  }
];

export const INITIAL_SETTINGS: LauncherSettings = {
  downloadSource: 'bmclapi',
  // 空串表示「未指定外部 Java」：启动器只用自己下载安装的运行时。
  // 这里不能放假路径——探测路径来自设置快照，假路径会显示成一个不存在的 Java。
  javaPath: '',
  maxMemoryMb: 8192,
  minMemoryMb: 2048,
  autoDownloadJava: true,
  instancesDirectory: 'Ninevoxel/instances/',
  javaRuntimeDirectory: 'Ninevoxel/javaruntime/',
  theme: 'dark',
  accentTheme: 'emerald',
  fullscreen: false,
  resolutionWidth: 1920,
  resolutionHeight: 1080,
  jvmPreset: 'aikar',
  enableDiscordRPC: true,
  closeOnLaunch: false
};

export const SAMPLE_LOGS: LogEntry[] = [
  { id: '1', timestamp: '10:24:01', level: 'INFO', source: 'Ninevoxel.Launcher', message: '正在加载独立实例分区: 常用模组包 / 1.21.4 原版极速优化' },
  { id: '2', timestamp: '10:24:02', level: 'INFO', source: 'Ninevoxel.Launcher', message: '已挂载本地专属隔离沙盒: instances/1.21.4-Fabric-Opt/.minecraft' },
  { id: '3', timestamp: '10:24:02', level: 'INFO', source: 'Ninevoxel.Launcher', message: '自动调用本地 Java 运行时: javaruntime/java-21/bin/java (GraalVM CE 21.0.3)' },
  { id: '4', timestamp: '10:24:03', level: 'INFO', source: 'Ninevoxel.Launcher', message: 'JVM 参数校验完成: -XX:+UseG1GC -Xmx6144M' },
  { id: '5', timestamp: '10:24:03', level: 'INFO', source: 'FabricLoader', message: 'Loading 18 mods from instances/1.21.4-Fabric-Opt/.minecraft/mods/' },
  { id: '6', timestamp: '10:24:04', level: 'INFO', source: 'Minecraft', message: 'Setting user: Alex_Ninevoxel (Microsoft OAuth2 Token Valid)' },
  { id: '7', timestamp: '10:24:05', level: 'INFO', source: 'Minecraft', message: 'LWJGL Version: 3.3.3 build 5' },
  { id: '8', timestamp: '10:24:06', level: 'INFO', source: 'Sodium', message: 'Hardware acceleration detected: NVIDIA GeForce RTX 4070 Laptop GPU / OpenGL 4.6.0' },
  { id: '9', timestamp: '10:24:07', level: 'INFO', source: 'Iris', message: 'Found active shader: ComplementaryReimagined_r5.2.2.zip in instances/.../shaderpacks' },
  { id: '10', timestamp: '10:24:08', level: 'INFO', source: 'Minecraft', message: 'Sound engine started.' },
  { id: '11', timestamp: '10:24:09', level: 'INFO', source: 'Minecraft', message: 'Reloading ResourceManager: Default, Faithful32x, FreshAnimations' },
  { id: '12', timestamp: '10:24:11', level: 'INFO', source: 'Minecraft', message: 'OpenAL initialized. Game window rendered at 1920x1080 (60.0 FPS vsync).' }
];

export const SAMPLE_CRASH_LOG: LogEntry[] = [
  { id: 'c1', timestamp: '11:15:22', level: 'INFO', source: 'Ninevoxel.Launcher', message: 'Starting process with PID 8941 in instances/1.21.4-Fabric-Opt/.minecraft...' },
  { id: 'c2', timestamp: '11:15:23', level: 'INFO', source: 'FabricLoader', message: 'Preparing mod loading pipeline...' },
  { id: 'c3', timestamp: '11:15:24', level: 'ERROR', source: 'FabricLoader', message: 'Incompatible mod set found in instances/.../mods/!' },
  { id: 'c4', timestamp: '11:15:24', level: 'ERROR', source: 'FabricLoader', message: 'Mod "Iris" (iris) version 1.8.0 requires fabricloader >=0.16.0, but only 0.15.11 is installed!' },
  { id: 'c5', timestamp: '11:15:24', level: 'FATAL', source: 'Minecraft', message: 'Failed to start the minecraft process: net.fabricmc.loader.impl.FormattedException: Mod resolution failed' },
  { id: 'c6', timestamp: '11:15:25', level: 'FATAL', source: 'Ninevoxel.Launcher', message: 'Game exited with code 1. Process terminated unexpectedly.' }
];

export const MC_VERSIONS_LIST = [
  { id: '1.21.4', type: 'release', date: '2024-12-03', supportedLoaders: ['fabric', 'neoforge', 'vanilla'] },
  { id: '1.21.3', type: 'release', date: '2024-10-22', supportedLoaders: ['fabric', 'neoforge', 'vanilla'] },
  { id: '1.21.1', type: 'release', date: '2024-08-08', supportedLoaders: ['fabric', 'forge', 'neoforge', 'quilt', 'vanilla'] },
  { id: '1.20.6', type: 'release', date: '2024-04-29', supportedLoaders: ['fabric', 'neoforge', 'vanilla'] },
  { id: '1.20.4', type: 'release', date: '2023-12-07', supportedLoaders: ['fabric', 'forge', 'neoforge', 'quilt', 'vanilla'] },
  { id: '1.20.1', type: 'release', date: '2023-06-12', supportedLoaders: ['forge', 'fabric', 'neoforge', 'quilt', 'vanilla'] },
  { id: '1.19.4', type: 'release', date: '2023-03-14', supportedLoaders: ['fabric', 'forge', 'quilt', 'vanilla'] },
  { id: '1.18.2', type: 'release', date: '2022-02-28', supportedLoaders: ['forge', 'fabric', 'quilt', 'vanilla'] },
  { id: '1.16.5', type: 'release', date: '2021-01-15', supportedLoaders: ['forge', 'fabric', 'vanilla'] },
  { id: '1.12.2', type: 'release', date: '2017-09-18', supportedLoaders: ['forge', 'vanilla'] },
  { id: '25w08a', type: 'snapshot', date: '2025-02-19', supportedLoaders: ['vanilla', 'fabric'] }
];
