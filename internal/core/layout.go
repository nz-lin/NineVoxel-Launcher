package core

// 核心目录下的顶层子目录名。
//
// 这些名称是磁盘布局的一部分，改动会影响既有数据；未正式发布前可以调整，
// 发布后必须通过迁移处理。
const (
	// versionsDirName 存放各版本的元数据、JAR 与 natives。
	versionsDirName = "versions"
	// librariesDirName 存放 Maven 布局的依赖库。
	librariesDirName = "libraries"
	// assetsDirName 存放资源索引与资源对象。
	assetsDirName = "assets"
	// indexDirName 存放版本索引。
	indexDirName = "core-index"
	// journalDirName 存放重建事务日志。
	journalDirName = "core-journal"

	// indexFileName 是版本索引的文件名。
	indexFileName = "versions.json"
	// indexSchemaVersion 是索引结构版本。
	indexSchemaVersion = 1

	// stagingSuffix 是版本目录安装暂存目录的后缀。
	//
	// 安装先在 `versions/<id>.staging` 完成，校验通过后再原子改名，
	// 这样任何时刻正式路径要么是旧内容、要么是完整的新内容。
	stagingSuffix = ".staging"
	// previousSuffix 是替换时旧目录的临时名字。
	previousSuffix = ".prev"
)
