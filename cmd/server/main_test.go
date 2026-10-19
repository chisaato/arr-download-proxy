package main

import (
	"bytes"
	"testing"
)

func TestDefaultConfigAndDebug(t *testing.T) {
	got := mustParse(t, nil)
	if got.configPath != "config.yaml" || got.debug {
		t.Fatalf("默认值 = %+v，期望 config.yaml 且 debug 关闭", got)
	}
}

func TestExplicitConfigFormats(t *testing.T) {
	cases := []struct {
		args  []string
		path  string
		debug bool
	}{
		{[]string{"--config", "custom.yaml"}, "custom.yaml", false},
		{[]string{"--config=custom.yaml"}, "custom.yaml", false},
		{[]string{"-c", "custom.yaml"}, "custom.yaml", false},
		{[]string{"-c=custom.yaml"}, "custom.yaml", false},
		{[]string{"--config", "-custom.yaml"}, "-custom.yaml", false},
		{[]string{"--debug", "--config", "custom.yaml"}, "custom.yaml", true},
		{[]string{"--config=custom.yaml", "--debug=false"}, "custom.yaml", false},
	}
	for _, tc := range cases {
		got := mustParse(t, tc.args)
		if got.configPath != tc.path || got.debug != tc.debug {
			t.Fatalf("%v = %+v，期望 path=%q debug=%v", tc.args, got, tc.path, tc.debug)
		}
	}
}

func TestDebugFlag(t *testing.T) {
	on := mustParse(t, []string{"--debug"})
	if !on.debug || on.configPath != "config.yaml" {
		t.Fatalf("--debug 结果 = %+v", on)
	}
	alsoOn := mustParse(t, []string{"--debug=true"})
	if !alsoOn.debug || alsoOn.configPath != "config.yaml" {
		t.Fatalf("--debug=true 结果 = %+v", alsoOn)
	}
	off := mustParse(t, []string{"--debug=false"})
	if off.debug || off.configPath != "config.yaml" {
		t.Fatalf("--debug=false 结果 = %+v", off)
	}
}

func TestHelpDoesNotStart(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		_, called, out, err := parseCLI(t, args)
		if err != nil {
			t.Fatalf("%v 帮助应成功退出: %v\n%s", args, err, out)
		}
		if called {
			t.Fatalf("%v 不应启动服务", args)
		}
		for _, want := range []string{"--config", "--debug", "-c"} {
			if !bytes.Contains([]byte(out), []byte(want)) {
				t.Fatalf("%v 帮助缺少 %s:\n%s", args, want, out)
			}
		}
	}
}

func TestUnknownArgsFail(t *testing.T) {
	cases := [][]string{
		{"--unknown"},
		{"positional"},
		{"--config"},
	}
	for _, args := range cases {
		_, called, _, err := parseCLI(t, args)
		if err == nil || called {
			t.Fatalf("%v 应报错且不启动: called=%v err=%v", args, called, err)
		}
	}
}

func mustParse(t *testing.T, args []string) cliOptions {
	t.Helper()
	got, called, out, err := parseCLI(t, args)
	if err != nil || !called {
		t.Fatalf("%v 解析失败: called=%v err=%v\n%s", args, called, err, out)
	}
	return got
}

func parseCLI(t *testing.T, args []string) (cliOptions, bool, string, error) {
	t.Helper()
	var got cliOptions
	called := false
	cmd := newRootCommand(func(opts cliOptions) error {
		called = true
		got = opts
		return nil
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := executeRoot(cmd, args)
	out := stdout.String() + stderr.String()
	return got, called, out, err
}
