// Package loader resolves and prepares Minecraft mod loaders.
//
// Fabric and Quilt publish complete profile JSON documents, so their installation
// metadata can be written directly into the instance's versions directory.
// Forge and NeoForge require their official installer because modern versions
// generate client libraries through installer processors.
package loader
