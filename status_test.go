package gobox

import "testing"

func TestStatusCloneDoesNotShareMutableData(t *testing.T) {
	original := Status{
		Features: []FeatureResult{{Feature: Environment, State: Applied}},
		Warnings: []string{"warning"},
		Limits:   map[string]int64{"commands": 1},
	}
	clone := original.Clone()
	clone.Features[0].State = Disabled
	clone.Warnings[0] = "changed"
	clone.Limits["commands"] = 2
	if original.Features[0].State != Applied || original.Warnings[0] != "warning" || original.Limits["commands"] != 1 {
		t.Fatal("clone shares mutable status data")
	}
}

func TestServiceStatusCloneDoesNotShareMutableData(t *testing.T) {
	original := ServiceStatus{
		Templates: map[TemplateID]Status{"default": {Warnings: []string{"warning"}}},
		Limits:    map[string]int64{"owners": 1},
	}
	clone := original.Clone()
	status := clone.Templates["default"]
	status.Warnings[0] = "changed"
	clone.Templates["default"] = status
	clone.Limits["owners"] = 2
	if original.Templates["default"].Warnings[0] != "warning" || original.Limits["owners"] != 1 {
		t.Fatal("clone shares mutable service status data")
	}
}
