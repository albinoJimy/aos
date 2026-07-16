package sandbox

import (
	"context"
	"path"
	"strconv"
	"strings"
	"sync"
)

// FakeDriver é o driver de REFERÊNCIA determinista in-process (AOS-064). Modela o
// jail da microVM inteiramente em memória: um FS isolado (mapa de caminhos), uma
// tabela de symlinks e — crucialmente — um "host" que o jail NÃO consegue
// referenciar estruturalmente. Impõe as invariantes de isolamento (sem socket do
// host, sem namespace partilhado) e BLOQUEIA escape por path traversal, symlink
// para fora ou metacaractere de shell ANTES de qualquer resolução — nada alcança o
// host. É o driver usado nos testes; NUNCA usar em produção.
//
// Determinismo: ids de instância sequenciais; execução pura (sem relógio/rand/IO
// real). Concorrente-seguro (o -race guarda create/exec/destroy em paralelo).
type FakeDriver struct {
	mu sync.Mutex

	seq   int
	insts map[string]*fakeJail

	// host modela o FS do host: o jail NUNCA lê deste mapa. Toda a LEITURA do host
	// passa pelo funnel [FakeDriver.readHost], que incrementa hostTouch — logo
	// QUALQUER caminho (deliberado ou acidental) que leia o host faz a asserção
	// HostTouches()==0 falhar. Sem este funnel a asserção seria tautológica (o mapa
	// host inalcançável só por AUSÊNCIA de código); com ele, o sentinela tem poder
	// de refutação (ver TestSecurity_HostSentinelHasRefutationPower).
	host      map[string][]byte
	hostTouch int

	// hostSocketAccessed modela um acesso ao socket do host — SEMPRE falso na
	// execução. Só [FakeDriver.accessHostSocket] o marca; nenhum caminho de
	// produção o chama (a microVM não expõe o socket, ADR-004).
	hostSocketAccessed bool

	// escapeAttempts conta tentativas de escape bloqueadas (asserção de segurança).
	escapeAttempts int
}

// fakeJail é o estado privado de uma instância: o FS isolado, os symlinks e um env
// server-side onde uma credencial injectada por handle poderia viver (nunca lido
// pelo exec — o segredo não vaza para o resultado).
type fakeJail struct {
	files    map[string][]byte
	symlinks map[string]string // link (limpo, relativo) → target declarado
	env      map[string]string // injecção server-side (ADR-006); não ecoada
}

// NewFakeDriver constrói o driver fake vazio.
func NewFakeDriver() *FakeDriver {
	return &FakeDriver{
		insts: map[string]*fakeJail{},
		host:  map[string][]byte{},
	}
}

// Kind implementa [SandboxDriver].
func (*FakeDriver) Kind() DriverKind { return DriverFake }

// Create implementa [SandboxDriver]: arranca um jail in-memory e impõe as
// invariantes de isolamento. Fail-closed se a spec pedir socket/namespace do host.
func (d *FakeDriver) Create(_ context.Context, cap capability, spec Spec) (Instance, error) {
	if !cap.sanctioned() {
		return Instance{}, ErrUnsanctionedCapability
	}
	if !spec.Isolation.NoHostSocket {
		return Instance{}, ErrHostSocketForbidden
	}
	if !spec.Isolation.NoSharedNetNS || !spec.Isolation.NoSharedPIDNS {
		return Instance{}, ErrSharedNamespaceForbidden
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	id := "fake-" + spec.RunID + "-" + spec.StepID + "-" + strconv.Itoa(d.seq)
	jail := &fakeJail{
		files:    map[string][]byte{},
		symlinks: map[string]string{},
		env:      map[string]string{},
	}
	d.insts[id] = jail
	return Instance{
		ID:            id,
		Kind:          DriverFake,
		NoHostSocket:  true,
		NoSharedNetNS: true,
		NoSharedPIDNS: true,
		handle:        jail,
	}, nil
}

// Exec implementa [SandboxDriver]: corre a tool call DENTRO do jail. Valida
// primeiro o escape (traversal / symlink para fora / metacaractere) e devolve
// [ErrJailEscape] SEM tocar o host. O resultado é sempre untrusted.
func (d *FakeDriver) Exec(_ context.Context, cap capability, inst Instance, req ExecRequest) (ExecResult, error) {
	if !cap.sanctioned() {
		return ExecResult{}, ErrUnsanctionedCapability
	}
	jail, ok := inst.handle.(*fakeJail)
	if !ok || jail == nil {
		return ExecResult{}, ErrDriverUnavailable
	}
	call := req.Call

	// (a) Metacaracteres de shell no comando/args: o jail não invoca um shell e não
	// deixa um metacaractere quebrar para fora. Bloqueio fail-closed.
	if hasShellMetachar(call.Command) {
		return d.blockEscape()
	}
	for _, a := range call.Args {
		if hasShellMetachar(a) {
			return d.blockEscape()
		}
	}

	// (b) Path traversal: um caminho absoluto ou com ".." escapa a raiz do jail.
	if call.Path != "" {
		clean, err := jailClean(call.Path)
		if err != nil {
			return d.blockEscape()
		}
		// (c) Symlink para fora: resolve os symlinks em QUALQUER componente do
		// caminho (não só no caminho exacto) e bloqueia se um salto escapa o jail —
		// modela o escape por symlink em directório-pai (ex.: 'sub'->'../../host'
		// acedido via 'sub/f'). Devolve o caminho canónico (dentro do jail).
		resolved, err := resolveJail(jail, clean)
		if err != nil {
			return d.blockEscape()
		}
		clean = resolved
		// Escrita/leitura ficam CONTIDAS no mapa do jail — nunca no host.
		if call.Write != nil {
			d.mu.Lock()
			buf := make([]byte, len(call.Write))
			copy(buf, call.Write)
			jail.files[clean] = buf
			d.mu.Unlock()
			return newResult([]byte("wrote "+strconv.Itoa(len(call.Write))+" bytes to "+clean), nil, 0), nil
		}
		d.mu.Lock()
		content, exists := jail.files[clean]
		d.mu.Unlock()
		if !exists {
			// Ficheiro inexistente no jail: erro de execução (não é escape). O host
			// NUNCA é consultado como fallback.
			return newResult(nil, nil, 1), nil
		}
		out := make([]byte, len(content))
		copy(out, content)
		return newResult(out, []Artifact{{Name: clean, Data: out}}, 0), nil
	}

	// (d) Sem path: eco determinista do comando+args (o "trabalho" da tool). O env
	// server-side (credencial injectada) NÃO é ecoado — o segredo não vaza.
	stdout := call.Command
	if len(call.Args) > 0 {
		stdout += " " + strings.Join(call.Args, " ")
	}
	return newResult([]byte(stdout), nil, 0), nil
}

// Destroy implementa [SandboxDriver]: descarta o jail (idempotente). O overlay
// efémero (AOS-066) desaparece com ele — nada persiste entre execuções.
func (d *FakeDriver) Destroy(_ context.Context, cap capability, inst Instance) error {
	if !cap.sanctioned() {
		return ErrUnsanctionedCapability
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.insts, inst.ID)
	return nil
}

// readHost é o ÚNICO ponto de leitura do mapa host e CONTABILIZA cada acesso em
// hostTouch. O jail nunca o chama (não há caminho de código do jail que alcance o
// host); existe para que qualquer futura leitura do host — deliberada ou acidental
// — incremente o contador e faça a asserção HostTouches()==0 falhar. É o que dá
// PODER DE REFUTAÇÃO ao sentinela (sem ele a asserção seria tautológica).
func (d *FakeDriver) readHost(name string) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hostTouch++
	v, ok := d.host[name]
	return v, ok
}

// accessHostSocket modela um acesso ao socket de controlo do host e marca o
// sentinela hostSocketAccessed. Nenhum caminho de produção o chama (a microVM não
// expõe o socket, ADR-004); existe para dar poder de refutação à asserção
// !HostSocketAccessed().
func (d *FakeDriver) accessHostSocket() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hostSocketAccessed = true
}

// blockEscape regista e devolve o bloqueio de escape. Não toca o host.
func (d *FakeDriver) blockEscape() (ExecResult, error) {
	d.mu.Lock()
	d.escapeAttempts++
	d.mu.Unlock()
	return ExecResult{}, ErrJailEscape
}

// Symlink planta um symlink no jail de uma instância (uso de teste): link e target
// tal como um atacante os declararia. Um target que escape a raiz é bloqueado no Exec.
func (d *FakeDriver) Symlink(inst Instance, link, target string) {
	if jail, ok := inst.handle.(*fakeJail); ok && jail != nil {
		clean, err := jailClean(link)
		if err != nil {
			clean = link
		}
		d.mu.Lock()
		jail.symlinks[clean] = target
		d.mu.Unlock()
	}
}

// PlantHostFile coloca um ficheiro no "host" (uso de teste): o sentinela que o
// jail NUNCA deve alcançar.
func (d *FakeDriver) PlantHostFile(name string, data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.host[name] = data
}

// HostTouches devolve o nº de acessos ao host (asserção de segurança: DEVE ser 0 —
// não existe caminho de código que leia o mapa host a partir do jail).
func (d *FakeDriver) HostTouches() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hostTouch
}

// HostSocketAccessed reporta se o driver alguma vez acedeu ao socket do host
// (SEMPRE falso — a microVM não expõe o socket do host, ADR-004).
func (d *FakeDriver) HostSocketAccessed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hostSocketAccessed
}

// EscapeAttempts devolve o nº de tentativas de escape bloqueadas.
func (d *FakeDriver) EscapeAttempts() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.escapeAttempts
}

// InjectEnv coloca um valor no env server-side de uma instância (uso de teste, a
// modelar a injecção de credencial por handle — ADR-006). O exec nunca o ecoa.
func (d *FakeDriver) InjectEnv(inst Instance, key, value string) {
	if jail, ok := inst.handle.(*fakeJail); ok && jail != nil {
		d.mu.Lock()
		jail.env[key] = value
		d.mu.Unlock()
	}
}

// newResult é o único construtor de [ExecResult] no núcleo — força o taint
// untrusted por construção (o método [ExecResult.Taint] devolve sempre untrusted).
func newResult(stdout []byte, arts []Artifact, exit int) ExecResult {
	return ExecResult{Stdout: stdout, Artifacts: arts, ExitCode: exit}
}

// hasShellMetachar detecta um metacaractere de shell perigoso (injecção de
// comando / quebra de contexto). O jail não invoca shell; qualquer um destes é
// tratado como tentativa de escape.
func hasShellMetachar(s string) bool {
	if strings.Contains(s, "$(") || strings.Contains(s, "${") || strings.Contains(s, "..") {
		return true
	}
	return strings.ContainsAny(s, ";|&`$><\n\r\x00")
}

// resolveJail canonicaliza um caminho já limpo contra a tabela de symlinks do
// jail, resolvendo symlinks em QUALQUER componente (não só no caminho exacto).
// Devolve [ErrJailEscape] se algum salto de symlink apontar para fora do jail —
// modelando o escape encadeado / por directório-pai que um jail real resolve ao
// nível do SO. Limita o nº de saltos para não entrar em ciclo de symlinks.
func resolveJail(jail *fakeJail, clean string) (string, error) {
	const maxHops = 40
	cur := clean
	for hop := 0; hop < maxHops; hop++ {
		parts := strings.Split(cur, "/")
		rewrote := false
		for i := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			target, isLink := jail.symlinks[prefix]
			if !isLink {
				continue
			}
			// O alvo do symlink não pode escapar a raiz do jail.
			ct, err := jailClean(target)
			if err != nil {
				return "", ErrJailEscape
			}
			// Substitui o componente-symlink pelo alvo canónico e reavalia o resto
			// do caminho a partir daí.
			joined := ct
			if rest := parts[i+1:]; len(rest) > 0 {
				joined = ct + "/" + strings.Join(rest, "/")
			}
			next, err := jailClean(joined)
			if err != nil {
				return "", ErrJailEscape
			}
			cur = next
			rewrote = true
			break
		}
		if !rewrote {
			return cur, nil
		}
	}
	// Demasiados saltos: possível ciclo de symlinks — trata como escape (fail-closed).
	return "", ErrJailEscape
}

// jailClean normaliza um caminho RELATIVO ao jail e rejeita o que escape a raiz:
// caminhos absolutos, volumes (C:) e traversal por "..". Devolve o caminho limpo
// (slash-separated) ou erro se escapar.
func jailClean(p string) (string, error) {
	if p == "" {
		return "", ErrJailEscape
	}
	if strings.ContainsRune(p, '\x00') {
		return "", ErrJailEscape
	}
	// Normaliza separadores Windows para o modelo do jail.
	q := strings.ReplaceAll(p, "\\", "/")
	// Caminho absoluto POSIX ou volume Windows (C:) escapa a raiz do jail.
	if strings.HasPrefix(q, "/") {
		return "", ErrJailEscape
	}
	if len(q) >= 2 && q[1] == ':' {
		return "", ErrJailEscape
	}
	clean := path.Clean(q)
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == "/" || strings.HasPrefix(clean, "/") {
		return "", ErrJailEscape
	}
	return clean, nil
}
