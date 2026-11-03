package instance

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInstanceGroupsDefaultCreateAndCounts(t *testing.T) {
	s := testService(t)
	groups, err := s.ListInstanceGroups()
	if err != nil {
		t.Fatal(err)
	}
	if groups.DefaultGroup != defaultInstanceGroup {
		t.Fatalf("默认分组 = %q，期望 %q", groups.DefaultGroup, defaultInstanceGroup)
	}
	if len(groups.Items) != 1 || !groups.Items[0].IsDefault {
		t.Fatalf("初始分组 = %+v", groups.Items)
	}

	created, err := s.CreateInstanceGroup(CreateInstanceGroupRequest{Name: "整合包"})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Items) != 2 {
		t.Fatalf("创建后分组数 = %d，期望 2", len(created.Items))
	}
	if _, err := s.CreateInstanceGroup(CreateInstanceGroupRequest{Name: "整合包"}); err == nil {
		t.Fatal("重名分组应被拒绝")
	}

	config := testConfig("整合包实例")
	config.Partition = "整合包"
	item, err := s.CreateInstance(config)
	if err != nil {
		t.Fatal(err)
	}
	if item.Partition != "整合包" {
		t.Fatalf("实例分组 = %q", item.Partition)
	}
	groups, err = s.ListInstanceGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups.Items {
		if group.Name == "整合包" && group.InstanceCount != 1 {
			t.Fatalf("整合包数量 = %d，期望 1", group.InstanceCount)
		}
	}
}

func TestDeleteGroupRequiresMoveAndMigratesInstances(t *testing.T) {
	s := testService(t)
	if _, err := s.CreateInstanceGroup(CreateInstanceGroupRequest{Name: "临时组"}); err != nil {
		t.Fatal(err)
	}
	config := testConfig("临时实例")
	config.Partition = "临时组"
	item, err := s.CreateInstance(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteInstanceGroup(DeleteInstanceGroupRequest{Name: "临时组"}); err == nil {
		t.Fatal("非空分组未提供迁移目标时应被拒绝")
	}
	if _, err := s.DeleteInstanceGroup(DeleteInstanceGroupRequest{Name: "临时组", MoveTo: "不存在的组"}); err == nil {
		t.Fatal("迁移目标不存在时应被拒绝")
	}
	groups, err := s.DeleteInstanceGroup(DeleteInstanceGroupRequest{Name: "临时组", MoveTo: defaultInstanceGroup})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups.Items {
		if group.Name == "临时组" {
			t.Fatal("分组应已删除")
		}
	}
	instances, err := s.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, instance := range instances.Items {
		if instance.ID == item.ID {
			found = instance.Partition == defaultInstanceGroup
		}
	}
	if !found {
		t.Fatal("实例未迁移到目标分组")
	}
	if _, err := s.DeleteInstanceGroup(DeleteInstanceGroupRequest{Name: defaultInstanceGroup}); err == nil {
		t.Fatal("默认分组不能删除")
	}
}

func TestLegacyDefaultPartitionMigratesToDefaultGroup(t *testing.T) {
	s := testService(t)
	if _, err := s.ListInstanceGroups(); err != nil {
		t.Fatal(err)
	}
	legacy := Instance{
		Config: Config{
			Name: "旧实例", MCVersion: "1.21.4", Loader: "vanilla",
			Partition: legacyDefaultPartition, Icon: "🟩", MemoryMB: 4096,
		},
		ID: "inst-legacy", FolderPath: "instances/inst-legacy/.minecraft",
		InstallationState: "not_installed", CreatedAtMS: time.Now().UnixMilli(),
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO instances(id,config_json,selected,created_at_ms,updated_at_ms) VALUES(?,?,0,?,?)`,
		legacy.ID, string(raw), legacy.CreatedAtMS, legacy.CreatedAtMS,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}

	reopened := &InstanceService{root: s.root}
	t.Cleanup(func() { _ = reopened.ServiceShutdown() })
	list, err := reopened.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Partition != defaultInstanceGroup {
		t.Fatalf("旧实例分组未迁移：%+v", list.Items)
	}
	groups, err := reopened.ListInstanceGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups.Items {
		if group.Name == legacyDefaultPartition {
			t.Fatal("旧默认分区不应作为独立分组存在")
		}
	}
}
