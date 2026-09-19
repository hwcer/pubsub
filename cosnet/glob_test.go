package cosnet

import "testing"

// 🔴 回归:> 通配曾整体失效——QuoteMeta 不转义裸 >,旧实现 ReplaceAll(`\>`,.+) 
// 永远匹配不上,订阅 a.> 编译出 ^a\.>$ 只匹配字面量
func TestCompileWildcardGreaterThan(t *testing.T) {
	re := compileWildcard("a.>")
	if !re.MatchString("a.b") || !re.MatchString("a.b.c") {
		t.Fatal("a.> 应匹配 a. 下所有层级")
	}
	if re.MatchString("a") {
		t.Fatal("a.> 不应匹配 a 本身")
	}
}

func TestCompileWildcardStar(t *testing.T) {
	re := compileWildcard("a.*")
	if !re.MatchString("a.b") {
		t.Fatal("a.* 应匹配 a.b")
	}
	if re.MatchString("a.b.c") {
		t.Fatal("a.* 不应跨层匹配 a.b.c")
	}
	if re.MatchString("a") {
		t.Fatal("a.* 不应匹配 a 本身")
	}
}

// 字面量点号必须转义:a.b 不应匹配 axb
func TestCompileWildcardEscapesLiteral(t *testing.T) {
	re := compileWildcard("a.b")
	if re.MatchString("axb") {
		t.Fatal("点号应被转义,axb 不应匹配 a.b")
	}
}
