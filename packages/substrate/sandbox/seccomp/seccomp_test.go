package seccomp

import (
	"strings"
	"testing"
)

// TestLoad_EmbeddedProfileIsDefaultDeny confirma que o perfil embebido carrega, é
// default-deny e tem uma allowlist não-vazia.
func TestLoad_EmbeddedProfileIsDefaultDeny(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("Load() erro inesperado: %v", err)
	}
	if p.Default() != ActionDeny {
		t.Fatalf("default = %q, quero deny", p.Default())
	}
	if got := len(p.AllowedSyscalls()); got == 0 {
		t.Fatal("allowlist vazia: perfil mínimo tem de listar as syscalls permitidas")
	}
	if p.VersionTag() == "" {
		t.Fatal("versão vazia")
	}
}

// TestAllows_MinimalAllowedPass_OthersBlocked é o teste NEGATIVO exigido: as
// syscalls mínimas passam; QUALQUER syscall fora da allowlist é bloqueada
// (default-deny), incluindo syscalls perigosas e nomes inexistentes.
func TestAllows_MinimalAllowedPass_OthersBlocked(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	tests := []struct {
		syscall string
		want    bool
	}{
		// Mínimas permitidas (têm de passar).
		{"read", true},
		{"write", true},
		{"openat", true},
		{"close", true},
		{"exit_group", true},
		{"futex", true},
		// Fora da allowlist — bloqueadas por omissão (default-deny). ptrace/mount/
		// reboot/execve/socket são superfície de evasão/escape/rede: NUNCA permitidas.
		{"ptrace", false},
		{"mount", false},
		{"reboot", false},
		{"execve", false},
		{"socket", false},
		{"init_module", false},
		{"kexec_load", false},
		{"setuid", false},
		// Nome inexistente e syscall vazia — bloqueadas (fail-closed).
		{"totally_made_up_syscall", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := p.Allows(tc.syscall); got != tc.want {
			t.Errorf("Allows(%q) = %v, quero %v", tc.syscall, got, tc.want)
		}
		wantAction := ActionDeny
		if tc.want {
			wantAction = ActionAllow
		}
		if got := p.Decision(tc.syscall); got != wantAction {
			t.Errorf("Decision(%q) = %v, quero %v", tc.syscall, got, wantAction)
		}
	}
}

// TestHash_StableAndVersioned confirma que o hash é determinista call-a-call e que
// a Version é "tag#digest12" — o valor gravado no manifesto.
func TestHash_StableAndVersioned(t *testing.T) {
	p1, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	p2, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if p1.Hash() != p2.Hash() {
		t.Fatalf("hash instável: %q != %q", p1.Hash(), p2.Hash())
	}
	if len(p1.Hash()) != 64 {
		t.Fatalf("hash não é sha256 hex de 64 chars: len=%d", len(p1.Hash()))
	}
	if !strings.HasPrefix(p1.Version(), p1.VersionTag()+"#") {
		t.Fatalf("Version() = %q, quero prefixo %q#", p1.Version(), p1.VersionTag())
	}
	if !strings.HasPrefix(p1.Hash(), strings.TrimPrefix(p1.Version(), p1.VersionTag()+"#")) {
		t.Fatalf("Version() digest12 %q não é prefixo do Hash() %q", p1.Version(), p1.Hash())
	}
}

// TestParse_TamperEvident confirma que alterar a allowlist muda o digest (o hash é
// tamper-evident: o manifesto detectaria um perfil adulterado).
func TestParse_TamperEvident(t *testing.T) {
	base := `{"version":"t/v1","default_action":"deny","allowed_syscalls":["read","write"]}`
	tampered := `{"version":"t/v1","default_action":"deny","allowed_syscalls":["read","write","ptrace"]}`
	pb, err := Parse([]byte(base))
	if err != nil {
		t.Fatalf("Parse(base): %v", err)
	}
	pt, err := Parse([]byte(tampered))
	if err != nil {
		t.Fatalf("Parse(tampered): %v", err)
	}
	if pb.Hash() == pt.Hash() {
		t.Fatal("digest não mudou ao adicionar uma syscall: não é tamper-evident")
	}
	if pb.Allows("ptrace") {
		t.Fatal("perfil base não devia permitir ptrace")
	}
}

// TestParse_OrderIndependentDigest confirma que o digest é canónico: a ordem das
// syscalls no JSON não altera o hash (determinismo).
func TestParse_OrderIndependentDigest(t *testing.T) {
	a := `{"version":"t/v1","default_action":"deny","allowed_syscalls":["read","write","close"]}`
	b := `{"version":"t/v1","default_action":"deny","allowed_syscalls":["close","write","read"]}`
	pa, err := Parse([]byte(a))
	if err != nil {
		t.Fatalf("Parse(a): %v", err)
	}
	pb, err := Parse([]byte(b))
	if err != nil {
		t.Fatalf("Parse(b): %v", err)
	}
	if pa.Hash() != pb.Hash() {
		t.Fatalf("digest depende da ordem: %q != %q", pa.Hash(), pb.Hash())
	}
}

// TestParse_FailClosed confirma que perfis fail-open ou malformados são REJEITADOS
// (fail-closed): default != deny, versão vazia, JSON inválido, entrada vazia.
func TestParse_FailClosed(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"default_allow", `{"version":"t/v1","default_action":"allow","allowed_syscalls":["read"]}`},
		{"default_vazio", `{"version":"t/v1","allowed_syscalls":["read"]}`},
		{"versao_vazia", `{"version":"","default_action":"deny","allowed_syscalls":["read"]}`},
		{"json_invalido", `{`},
		{"syscall_vazia", `{"version":"t/v1","default_action":"deny","allowed_syscalls":["read",""]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.json)); err == nil {
				t.Fatalf("Parse(%s) devia falhar fail-closed", tc.name)
			}
		})
	}
}

// TestAllowedSyscalls_CopyIsolated confirma que mutar o slice devolvido não afecta
// o perfil (imutabilidade defensiva).
func TestAllowedSyscalls_CopyIsolated(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	list := p.AllowedSyscalls()
	if len(list) == 0 {
		t.Fatal("allowlist vazia")
	}
	list[0] = "MUTATED"
	for _, s := range p.AllowedSyscalls() {
		if s == "MUTATED" {
			t.Fatal("mutação do slice devolvido afectou o perfil interno")
		}
	}
}
