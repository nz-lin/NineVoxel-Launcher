/**
 * 内存显示口径的唯一实现。
 *
 * 后端用 `memoryMb = 0` 表示「跟随全局」——这是存储层的约定，不是「0 MB」。
 * 早期各界面直接渲染这个字段，跟随全局的实例就显示成了「0 MB」。
 *
 * 三个显示位置（顶部状态栏、启动页、实例卡片）如果各写一份判断，迟早会漂移，
 * 因此统一收在这里。
 */

/** 计算内存显示所需的最小输入。 */
export interface MemoryDisplayInput {
  /** 实例配置的内存来源。 */
  memoryMode?: 'global' | 'custom' | string | null
  /** 实例配置的内存值（MB）；跟随全局时为 0。 */
  memoryMb?: number | null
  /** 全局默认最大内存（MB）；未知时传 undefined。 */
  globalMemoryMb?: number | null
}

/** 一次内存显示的解析结果。 */
export interface MemoryDisplay {
  /** 是否使用全局值。 */
  followsGlobal: boolean
  /** 实际生效的内存（MB）；0 表示连全局值也未知。 */
  effectiveMb: number
  /** 是否已知具体数值（false 时界面应显示「跟随全局」而不是 0）。 */
  known: boolean
}

/**
 * 解析实例应当显示的内存。
 *
 * 判定「跟随全局」的条件包含 `!memoryMb`：模式字段可能是旧数据里缺失的空值，
 * 此时只能靠数值判断。这比只看模式更稳，也不会把合法的自定义值误判。
 *
 * @param input 实例与全局配置
 * @returns 是否跟随全局、实际生效值、数值是否可知
 */
export function resolveMemoryDisplay(input: MemoryDisplayInput): MemoryDisplay {
  const followsGlobal = input.memoryMode !== 'custom' || !input.memoryMb
  if (!followsGlobal) {
    return { followsGlobal: false, effectiveMb: input.memoryMb ?? 0, known: true }
  }
  const globalMb = input.globalMemoryMb ?? 0
  return { followsGlobal: true, effectiveMb: globalMb, known: globalMb > 0 }
}

/**
 * 把解析结果格式化为界面文本。
 *
 * @param display 解析结果
 * @returns 例如 `8192 MB`（数值未知时为 `跟随全局`）
 */
export function formatMemoryDisplay(display: MemoryDisplay): string {
  return display.known ? `${display.effectiveMb} MB` : '跟随全局'
}
