package handler

import (
	"testing"

	"ops-platform/server/internal/model"
)

func TestWorseHealth(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{healthNormal, healthUnknown, healthUnknown},
		{healthUnknown, healthWarning, healthWarning},
		{healthWarning, healthError, healthError},
		{healthError, healthNormal, healthError},
		{healthNormal, healthNormal, healthNormal},
	}
	for _, tc := range cases {
		if got := worseHealth(tc.a, tc.b); got != tc.want {
			t.Fatalf("worseHealth(%s, %s) = %s，期望 %s", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestFindCycle(t *testing.T) {
	nodes := []model.TopologyNode{
		{ID: 1, Name: "入口网关"},
		{ID: 2, Name: "订单服务"},
		{ID: 3, Name: "订单库"},
		{ID: 4, Name: "对账服务"},
	}

	// 一条直链：网关 -> 服务 -> 库，不该报环
	chain := []model.TopologyEdge{
		{FromNodeID: 1, ToNodeID: 2},
		{FromNodeID: 2, ToNodeID: 3},
	}
	if cycle := findCycle(nodes, chain); cycle != nil {
		t.Fatalf("直链不应该有环，却得到 %v", cycle)
	}

	// 汇聚：两个上游指向同一个下游，也不是环
	diamond := append(chain, model.TopologyEdge{FromNodeID: 4, ToNodeID: 3})
	if cycle := findCycle(nodes, diamond); cycle != nil {
		t.Fatalf("汇聚结构不应该有环，却得到 %v", cycle)
	}

	// 真的成环：库又指回服务
	looped := append(diamond, model.TopologyEdge{FromNodeID: 3, ToNodeID: 2})
	cycle := findCycle(nodes, looped)
	if len(cycle) == 0 {
		t.Fatal("成环却没检测出来")
	}
	// 路径要闭合，第一个和最后一个是同一个节点，方便前端直接展示
	if cycle[0] != cycle[len(cycle)-1] {
		t.Fatalf("环路路径应闭合，实际 %v", cycle)
	}
}
