package ai

import "testing"

func TestEvalSimpleExpr(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"1+2", 3},
		{"8-3", 5},
		{"4*5", 20},
		{"10/2", 5},
		{"sqrt(16)", 4},
	}
	for _, tc := range cases {
		got, err := evalSimpleExpr(tc.expr)
		if err != nil {
			t.Fatalf("expr %s unexpected err: %v", tc.expr, err)
		}
		if got != tc.want {
			t.Fatalf("expr %s want %v got %v", tc.expr, tc.want, got)
		}
	}
}

func TestInferRole(t *testing.T) {
	if inferRole("帮我看go代码 bug") != RoleTech {
		t.Fatal("expected tech role")
	}
	if inferRole("怎么部署kafka和redis") != RoleOps {
		t.Fatal("expected ops role")
	}
	if inferRole("请解释一下协程原理") != RoleTutor {
		t.Fatal("expected tutor role")
	}
}

func TestDetectToolCall(t *testing.T) {
	name, _ := detectToolCall("现在几点")
	if name != "get_current_time" {
		t.Fatalf("expected get_current_time got %s", name)
	}
	name, _ = detectToolCall("北京天气")
	if name != "get_weather" {
		t.Fatalf("expected get_weather got %s", name)
	}
	name, _ = detectToolCall("计算 8/2")
	if name != "calculator" {
		t.Fatalf("expected calculator got %s", name)
	}
}
