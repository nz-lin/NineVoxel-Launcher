// Package loader 提供加载器元数据查询与法定的加载器安装能力。
//
// # 设计目标
//
// 加载器的「产物」必须由官方代码生成，本包不实现任何二进制变换逻辑：
//
//   - Fabric / Quilt 的官方元数据直接给出可启动的版本描述，写进版本目录即可；
//   - Forge / NeoForge 的客户端 jar 由其安装器内的 processor 生成，
//     本包负责按官方声明的顺序编排这些 processor。
//
// # 与官方安装器的关系
//
// 早期实现直接执行安装器的命令行入口（`--installClient`）。该入口是上游随时
// 可能移除的实现细节，因此改为：解析安装器内的 `install_profile.json`，
// 用安装器**自带**的 processor jar 逐个执行。
//
// 这不改变「产物由官方代码生成」这一前提，只是不再依赖那个命令行入口；
// processor 的 main class、classpath、参数与期望哈希全部来自官方配置。
//
// # 硬约束
//
//   - processor 必须按 `processors` 数组顺序执行，不可重排或并行；
//   - 每个 processor 的产出必须按 `outputs` 声明的 SHA-1 校验，
//     不符即失败，不写入清单；
//   - processor 依赖缓存于 `cache/processors/`：它们只在安装期使用，
//     不属于任何已安装版本的组成部分，不应进入安装清单；
//   - 交给 processor 的原版客户端 jar 必须是一份隔离副本：processor 会在输入
//     旁边写标记文件，直接使用共享缓存会污染其他实例共用的原版文件；
//   - 下载地址必须通过 download 包的源注册表校验，本包不维护第二份白名单。
//
// # 非目标
//
//   - 不实现自己的二进制变换（srg、SpecialSource、BINPATCH 等一律由官方
//     processor 完成）；
//   - 不维护加载器元数据服务：不缓存「加载器 × 版本 × MC 版本」的成品描述，
//     也不预生成产物；
//   - 不支持 `spec != 1` 的旧安装格式（解压 universal jar），它们需要另一条
//     代码路径。
package loader
