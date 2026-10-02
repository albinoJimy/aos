package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// AOS-446, Fase 0 — a fronteira do host (ADR-033 §2.1, emenda AOS-446).
//
// O DEFEITO que estes testes guardam, verificado em produção a 2026-09-26: o
// `aos-tls-sync.service` corria como ROOT (sem `User=`), com `KUBECONFIG=/etc/kubernetes/admin.conf`,
// o executável `/opt/aos/sync-tls.sh` — ficheiro do `aos` (0755) que o CD reescrevia a cada deploy.
// Quem escrevesse nele (o `aos`, a chave de deploy, quem aprovasse o environment production, ou
// quem fizesse merge de uma alteração ao script) ganhava root no host e cluster-admin no cluster
// na passagem diária seguinte. E root no host contorna o mandato (ADR-033 §2.1).
//
// A regra, para TODA a unidade que corre como root: o executável, os ficheiros de ambiente e o
// kubeconfig ficam fora do alcance do `aos` e do deploy. O teste não sabe que o sync-tls existe —
// deriva o conjunto das unidades — e prova-se contra a unidade que estava em produção.

// aos446Caminho resolve um caminho do repositório a partir de packages/cmd/aos-issuer.
func aos446Caminho(partes ...string) string {
	return filepath.Join(append([]string{"..", "..", ".."}, partes...)...)
}

func aos446Ler(t *testing.T, partes ...string) string {
	t.Helper()
	b, err := os.ReadFile(aos446Caminho(partes...))
	if err != nil {
		t.Fatalf("ler %s: %v", filepath.Join(partes...), err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// aos446Unidade é o que interessa de uma unidade systemd para a fronteira do root.
type aos446Unidade struct {
	user         string   // o ÚLTIMO User= — é o que o systemd aplica quando há vários
	nUser        int      // quantos User= a unidade tem
	execs        []string // 1.º token de cada Exec* (ExecStart, ExecStartPre, ExecStop, ...)
	linhasExec   []string // a linha de comando inteira de cada Exec*, sem os prefixos
	environment  []string // atribuições NOME=VALOR de todos os Environment=
	envFiles     []string // valores de EnvironmentFile=
	workDirs     []string // valores de WorkingDirectory=
	privilegiado bool     // algum Exec com prefixo `+` ou `!` (corre com privilégios apesar do User=)
}

var reAos446Exec = regexp.MustCompile(`^(ExecStart|ExecStartPre|ExecStartPost|ExecStop|ExecStopPost|ExecReload|ExecCondition)=\s*(.+)$`)

// aos446Atribuicoes parte o valor de um Environment= nas suas atribuições. O systemd aceita
// várias por linha, separadas por espaço, cada uma opcionalmente entre aspas duplas.
func aos446Atribuicoes(valor string) []string {
	var out []string
	var cur strings.Builder
	aspas := false
	for _, r := range valor {
		switch {
		case r == '"':
			aspas = !aspas
		case (r == ' ' || r == '\t') && !aspas:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func aos446Parse(texto string) aos446Unidade {
	var u aos446Unidade
	for _, linha := range strings.Split(texto, "\n") {
		l := strings.TrimSpace(linha)
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, ";") {
			continue
		}
		switch {
		case strings.HasPrefix(l, "User="):
			u.user = strings.TrimSpace(strings.TrimPrefix(l, "User="))
			u.nUser++
		case strings.HasPrefix(l, "Environment="):
			u.environment = append(u.environment, aos446Atribuicoes(strings.TrimPrefix(l, "Environment="))...)
		case strings.HasPrefix(l, "EnvironmentFile="):
			u.envFiles = append(u.envFiles, strings.TrimLeft(strings.TrimPrefix(l, "EnvironmentFile="), "-"))
		case strings.HasPrefix(l, "WorkingDirectory="):
			u.workDirs = append(u.workDirs, strings.TrimLeft(strings.TrimPrefix(l, "WorkingDirectory="), "-"))
		default:
			if m := reAos446Exec.FindStringSubmatch(l); m != nil {
				alvo := strings.TrimSpace(m[2])
				// Prefixos do systemd: `-` ignora a falha, `@` troca o argv[0], `:` desliga a
				// expansão; `+` e `!` correm com privilégios TOTAIS mesmo com User=.
				for alvo != "" && strings.ContainsRune("-@:+!", rune(alvo[0])) {
					if alvo[0] == '+' || alvo[0] == '!' {
						u.privilegiado = true
					}
					alvo = alvo[1:]
				}
				u.linhasExec = append(u.linhasExec, alvo)
				// Um Exec só de prefixos não nomeia executável: fica como "" e é recusado abaixo
				// (fora de /usr/local/sbin), em vez de rebentar o teste.
				primeiro := ""
				if campos := strings.Fields(alvo); len(campos) > 0 {
					primeiro = campos[0]
				}
				u.execs = append(u.execs, primeiro)
			}
		}
	}
	return u
}

func (u aos446Unidade) comoRoot() bool {
	return u.user == "" || u.user == "root" || u.user == "0" || u.privilegiado
}

// aos446KubeconfigMinimo é o ÚNICO kubeconfig que uma unidade root pode fixar: a ServiceAccount
// do tls-sync-rbac.yaml. Allowlist exacta — uma lista de proibidos deixava passar o
// controller-manager.conf e o scheduler.conf, que também são credenciais largas do kubeadm.
const aos446KubeconfigMinimo = "/etc/aos/kube/aos-tls-sync.kubeconfig"

// aos446KubeconfigProibido: o que o sync-tls.sh usava ou procurava antes do AOS-446.
var aos446KubeconfigProibido = []string{"/etc/kubernetes/admin.conf", "/etc/kubernetes/super-admin.conf", "/root/.kube/"}

// aos446VariavelProibida: variáveis que fazem o processo root carregar código ou procurar
// executáveis onde outro utilizador escreve, seja qual for o valor.
func aos446VariavelProibida(nome string) bool {
	return nome == "BASH_ENV" || nome == "ENV" || nome == "PATH" || strings.HasPrefix(nome, "LD_")
}

// aos446ArvoreDoAos diz se um valor nomeia um caminho que o `aos` (ou o deploy) escreve.
func aos446ArvoreDoAos(valor string) bool {
	return strings.Contains(valor, "/opt/aos/") || strings.HasSuffix(valor, "/opt/aos") ||
		strings.Contains(valor, "/home/")
}

// reAos446DirInteiro: formas de rsync que levam o topo de deploy/server INTEIRO — glob, chaves,
// «.», ou o directório sem nada a seguir. Qualquer uma voltava a enviar o executável do root.
var reAos446DirInteiro = regexp.MustCompile(`deploy/server/?(\*|\{|\.(/|"|'|\s|$)|"|'|\s|$)`)

// aos446NoDeploy diz se a fonte de um executável do root chega ao servidor pelo deploy.yml, em
// QUALQUER forma: pelo nome (com ou sem `deploy/server/`, numa lista de um `for`), ou por uma
// forma que leve o directório inteiro. Linhas de comentário não contam.
func aos446NoDeploy(deployYML, fonte string) (bool, string) {
	// O nome do ficheiro, ou um glob sobre ele (`sync-tls.sh`, `sync-tls.*`, `sync-tls*`). Não o
	// nome nu: «a ponte sync-tls parada» numa mensagem de erro do deploy não entrega nada.
	reNome := regexp.MustCompile(regexp.QuoteMeta(strings.TrimSuffix(fonte, ".sh")) + `(\.sh|\.\*|\*)`)
	for _, linha := range strings.Split(deployYML, "\n") {
		l := strings.TrimSpace(linha)
		// Comentários e títulos de passo (`- name: Sincronizar deploy/server -> …`) não entregam nada.
		if strings.HasPrefix(l, "#") || strings.HasPrefix(l, "- name:") || strings.HasPrefix(l, "name:") {
			continue
		}
		if reNome.MatchString(l) {
			return true, l
		}
		if reAos446DirInteiro.MatchString(l) {
			return true, l
		}
	}
	return false, ""
}

// aos446Violacoes devolve o que torna uma unidade ROOT alcançável por quem não é root. Vazio =
// a unidade respeita a fronteira (ou não corre como root).
func aos446Violacoes(nome, texto, deployYML string) []string {
	u := aos446Parse(texto)
	var v []string
	// User= repetido: o systemd aplica o ÚLTIMO. Um `User=aos` seguido de `User=root` é root, e
	// quem lê só o primeiro (o lint antes desta revisão, `grep -m1`) classificava-a mal.
	if u.nUser > 1 {
		v = append(v, nome+": tem "+strconv.Itoa(u.nUser)+" User= — o systemd aplica o ultimo; declara um so")
	}
	if !u.comoRoot() {
		return v
	}
	if len(u.execs) == 0 {
		v = append(v, nome+": corre como root e nao tem Exec* — o teste nao verificou nada")
	}
	for _, linha := range u.linhasExec {
		// A linha INTEIRA: `/usr/bin/bash /opt/aos/x.sh` corre como root um ficheiro do `aos`
		// tanto como `/opt/aos/x.sh`.
		if aos446ArvoreDoAos(linha) {
			v = append(v, nome+": o root executa "+linha+" — numa arvore do `aos`/do deploy")
		}
		for _, p := range aos446KubeconfigProibido {
			if strings.Contains(linha, p) {
				v = append(v, nome+": usa "+p+" — a ponte so precisa de `get` num secret (tls-sync-rbac.yaml)")
			}
		}
	}
	for _, alvo := range u.execs {
		if !strings.HasPrefix(alvo, "/usr/local/sbin/aos-") {
			v = append(v, nome+": executavel do root "+alvo+" fora de /usr/local/sbin/aos-<nome> (o caminho que o root instala)")
			continue
		}
		fonte := strings.TrimPrefix(alvo, "/usr/local/sbin/aos-") + ".sh"
		if no, l := aos446NoDeploy(deployYML, fonte); no {
			v = append(v, nome+": a fonte deploy/server/"+fonte+" do executavel do root chega ao servidor pelo deploy ("+l+") — o CD reescreve-a")
		}
	}
	for _, f := range u.envFiles {
		// Um EnvironmentFile do `aos` injecta BASH_ENV, PATH ou KUBECONFIG num processo root.
		if aos446ArvoreDoAos(f) {
			v = append(v, nome+": EnvironmentFile "+f+" de um processo root vive numa arvore do `aos`")
		}
	}
	for _, d := range u.workDirs {
		if aos446ArvoreDoAos(d) {
			v = append(v, nome+": WorkingDirectory "+d+" de um processo root vive numa arvore do `aos`")
		}
	}
	for _, e := range u.environment {
		chave, valor, _ := strings.Cut(e, "=")
		switch {
		case aos446VariavelProibida(chave):
			v = append(v, nome+": Environment "+chave+"= num processo root — carrega codigo ou procura executaveis fora do controlo do root")
		case chave == "KUBECONFIG" && valor != aos446KubeconfigMinimo:
			v = append(v, nome+": KUBECONFIG="+valor+" — o unico permitido a uma unidade root e "+aos446KubeconfigMinimo)
		case aos446ArvoreDoAos(valor):
			v = append(v, nome+": Environment "+e+" aponta para uma arvore do `aos`")
		}
	}
	return v
}

// A unidade que estava em produção a 2026-09-26 (verificada no host). É o controlo de mutação:
// se o verificador deixar de a recusar, deixou de guardar o que quer que seja.
const aos446UnidadeDeProducao = `[Unit]
Description=Sincroniza o certificado TLS do no AOS a partir do cert-manager
After=docker.service

[Service]
Type=oneshot
Environment=KUBECONFIG=/etc/kubernetes/admin.conf
ExecStart=/opt/aos/sync-tls.sh
`

func TestAOS446_UnidadesRootForaDoAlcanceDoAos(t *testing.T) {
	deployYML := aos446Ler(t, ".github", "workflows", "deploy.yml")
	dir := aos446Caminho("deploy", "server", "systemd")
	entradas, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ler %s: %v", dir, err)
	}
	var root, falhas []string
	for _, e := range entradas {
		if !strings.HasSuffix(e.Name(), ".service") {
			continue
		}
		texto := aos446Ler(t, "deploy", "server", "systemd", e.Name())
		if aos446Parse(texto).comoRoot() {
			root = append(root, e.Name())
		}
		falhas = append(falhas, aos446Violacoes(e.Name(), texto, deployYML)...)
	}
	sort.Strings(root)
	// ANTI-VACUIDADE: o aos-tls-sync é root por necessidade (escreve no edge e recarrega-o mesmo
	// que o `aos` saia do grupo docker — decisão 2 do AOS-446). Se deixar de ser classificado como
	// root, o teste passaria sem ter verificado a unidade que o motivou.
	if !aos446Contem(root, "aos-tls-sync.service") {
		t.Fatalf("aos-tls-sync.service nao classificada como root (root=%v) — o verificador ficou vacuo", root)
	}
	for _, f := range falhas {
		t.Error(f)
	}
}

func TestAOS446_VerificadorRecusaAUnidadeDeProducao(t *testing.T) {
	v := aos446Violacoes("aos-tls-sync.service", aos446UnidadeDeProducao, "            deploy/server/sync-tls.sh \\\n")
	quer := []string{"numa arvore do `aos`", "fora de /usr/local/sbin", "/etc/kubernetes/admin.conf"}
	for _, q := range quer {
		achou := false
		for _, x := range v {
			if strings.Contains(x, q) {
				achou = true
			}
		}
		if !achou {
			t.Errorf("a unidade de producao nao foi recusada por %q; violacoes=%v", q, v)
		}
	}
	// A regra INVERSA do rsync: a fonte do executável do root no deploy é recusada...
	instalada := "[Service]\nExecStart=/usr/local/sbin/aos-sync-tls\n"
	if v := aos446Violacoes("x.service", instalada, "  deploy/server/sync-tls.sh \\\n"); len(v) != 1 || !strings.Contains(v[0], "chega ao servidor pelo deploy") {
		t.Errorf("a fonte no rsync nao foi recusada: %v", v)
	}
	// ... e uma menção em comentário não conta como entrega.
	if v := aos446Violacoes("x.service", instalada, "  # o sync-tls.sh nao viaja: deploy/server/sync-tls.sh\n"); len(v) != 0 {
		t.Errorf("comentario tratado como entrega: %v", v)
	}
	// A linha inteira conta: um interpretador do sistema a correr um ficheiro do `aos`.
	if v := aos446Violacoes("x.service", "[Service]\nExecStart=/usr/bin/bash /opt/aos/x.sh\n", ""); len(v) == 0 {
		t.Error("ExecStart=/usr/bin/bash /opt/aos/x.sh nao foi recusado")
	}
	// `+` corre com privilégios totais mesmo com User=aos.
	if v := aos446Violacoes("x.service", "[Service]\nUser=aos\nExecStart=+/opt/aos/x.sh\n", ""); len(v) == 0 {
		t.Error("ExecStart=+ sob User=aos nao foi tratado como root")
	}
	// Um EnvironmentFile do `aos` num processo root é código do `aos` como root (BASH_ENV).
	if v := aos446Violacoes("x.service", "[Service]\nEnvironmentFile=-/opt/aos/.env\nExecStart=/usr/local/sbin/aos-x\n", ""); len(v) == 0 {
		t.Error("EnvironmentFile sob /opt/aos num processo root nao foi recusado")
	}
	// Revisão de segurança da Fase 0: cada forma de pôr código ou caminhos do `aos` num processo
	// root, e cada forma de o rsync voltar a levar o executável, tem de ser recusada.
	mutacoes := []struct{ caso, unidade, deploy, quer string }{
		{"BASH_ENV", "Environment=BASH_ENV=/opt/aos/.bashenv", "", "BASH_ENV"},
		{"BASH_ENV noutro sitio", "Environment=BASH_ENV=/etc/aos/x", "", "BASH_ENV"},
		{"ENV", "Environment=ENV=/etc/aos/x", "", "ENV="},
		{"LD_PRELOAD", "Environment=LD_PRELOAD=/usr/lib/x.so", "", "LD_PRELOAD"},
		{"PATH", "Environment=PATH=/opt/aos/bin:/usr/bin", "", "PATH"},
		{"PATH entre aspas, 2.a atribuicao", `Environment="A=1" "PATH=/usr/bin"`, "", "PATH"},
		{"KUBECONFIG largo nao listado", "Environment=KUBECONFIG=/etc/kubernetes/controller-manager.conf", "", "controller-manager.conf"},
		{"KUBECONFIG scheduler", "Environment=KUBECONFIG=/etc/kubernetes/scheduler.conf", "", "scheduler.conf"},
		{"outra variavel sob /opt/aos", "Environment=CFG=/opt/aos/x.conf", "", "arvore do `aos`"},
		{"WorkingDirectory", "WorkingDirectory=/opt/aos", "", "WorkingDirectory"},
		{"WorkingDirectory home", "WorkingDirectory=-/home/aos", "", "WorkingDirectory"},
		{"rsync por glob", "", "  rsync -az deploy/server/*.sh host:/opt/aos/\n", "chega ao servidor"},
		{"rsync do topo inteiro", "", "  rsync -az deploy/server/ host:/opt/aos/\n", "chega ao servidor"},
		{"rsync do topo por ponto", "", "  rsync -az deploy/server/. host:/opt/aos/\n", "chega ao servidor"},
		{"rsync por chaves", "", "  rsync -az deploy/server/{a,b}.sh host:/opt/aos/\n", "chega ao servidor"},
		{"rsync por lista de for", "", "  for f in sync-tls.sh alerta-nhi.sh; do\n", "chega ao servidor"},
	}
	for _, m := range mutacoes {
		texto := "[Service]\nExecStart=/usr/local/sbin/aos-sync-tls\n" + m.unidade + "\n"
		v := aos446Violacoes("x.service", texto, m.deploy)
		achou := false
		for _, x := range v {
			if strings.Contains(x, m.quer) {
				achou = true
			}
		}
		if !achou {
			t.Errorf("mutacao %q nao recusada (queria %q): %v", m.caso, m.quer, v)
		}
	}
	// O kubeconfig mínimo, e só ele, passa.
	if v := aos446Violacoes("x.service", "[Service]\nEnvironment=KUBECONFIG="+aos446KubeconfigMinimo+"\nExecStart=/usr/local/sbin/aos-sync-tls\n", ""); len(v) != 0 {
		t.Errorf("o kubeconfig minimo foi recusado: %v", v)
	}
	// User= repetido: o systemd aplica o ÚLTIMO — `User=aos` e depois `User=root` é root.
	dois := "[Service]\nUser=aos\nUser=root\nExecStart=/opt/aos/x.sh\n"
	if !aos446Parse(dois).comoRoot() {
		t.Error("User=aos seguido de User=root nao foi classificado como root")
	}
	if v := aos446Violacoes("x.service", dois, ""); len(v) < 2 {
		t.Errorf("User= repetido nao foi recusado como root e como ambiguo: %v", v)
	}
	if v := aos446Violacoes("x.service", "[Service]\nUser=root\nUser=aos\nExecStart=/opt/aos/x.sh\n", ""); len(v) != 1 || !strings.Contains(v[0], "User=") {
		t.Errorf("User= repetido (ultimo aos) devia dar so a ambiguidade: %v", v)
	}
	// Uma unidade do `aos` não é da conta deste verificador (o lint guarda a entrega dela).
	if v := aos446Violacoes("x.service", "[Service]\nUser=aos\nExecStart=/opt/aos/x.sh\n", ""); len(v) != 0 {
		t.Errorf("unidade do aos recusada: %v", v)
	}
}

// O script que a unidade root corre também não pode recuar para o admin.conf: era o que fazia
// quando o KUBECONFIG faltava (`for c in /etc/kubernetes/admin.conf /root/.kube/config`).
func TestAOS446_SyncTLSNaoRecuaParaOAdminConf(t *testing.T) {
	script := aos446Ler(t, "deploy", "server", "sync-tls.sh")
	unidade := aos446Parse(aos446Ler(t, "deploy", "server", "systemd", "aos-tls-sync.service"))

	reAtrib := regexp.MustCompile(`(?:^|[\s;])(?:export\s+)?KUBECONFIG(?:_MINIMO)?=("?)([^"\s;]*)`)
	var minimo string
	for _, linha := range strings.Split(script, "\n") {
		l := strings.TrimSpace(linha)
		if strings.HasPrefix(l, "#") {
			continue
		}
		if strings.HasPrefix(l, "for ") {
			for _, p := range aos446KubeconfigProibido {
				if strings.Contains(l, p) {
					t.Errorf("sync-tls.sh percorre %s a procura de kubeconfig: %q", p, l)
				}
			}
		}
		for _, m := range reAtrib.FindAllStringSubmatch(l, -1) {
			for _, p := range aos446KubeconfigProibido {
				if strings.Contains(m[2], p) {
					t.Errorf("sync-tls.sh atribui KUBECONFIG=%s: %q", m[2], l)
				}
			}
			if strings.HasPrefix(l, "KUBECONFIG_MINIMO=") {
				minimo = m[2]
			}
		}
	}
	if minimo == "" {
		t.Fatal("sync-tls.sh sem KUBECONFIG_MINIMO= — o teste nao sabe que kubeconfig o script usa por omissao")
	}
	// O kubeconfig da unidade e o do script são o MESMO ficheiro, e é do root.
	quer := "KUBECONFIG=" + minimo
	if !aos446Contem(unidade.environment, quer) {
		t.Errorf("a unidade nao fixa %s (Environment=%v)", quer, unidade.environment)
	}
	if !strings.HasPrefix(minimo, "/etc/aos/") {
		t.Errorf("KUBECONFIG_MINIMO=%s fora de /etc/aos (arvore do root)", minimo)
	}
	// O manifesto RBAC que dá à conta o seu único poder existe, e é só `get` no secret que o
	// script lê.
	rbac := aos446Ler(t, "deploy", "server", "tls-sync-rbac.yaml")
	for _, q := range []string{`resourceNames: ["aos-node-tls"]`, `verbs: ["get"]`, "namespace: default"} {
		if !strings.Contains(rbac, q) {
			t.Errorf("tls-sync-rbac.yaml sem %q", q)
		}
	}
	// UMA regra e UM verbo: uma segunda regra (outro secret, `list`, outro recurso) alargava a
	// conta sem que as linhas acima dessem por isso.
	if n := strings.Count(rbac, "verbs:"); n != 1 {
		t.Errorf("tls-sync-rbac.yaml tem %d listas de verbos — esperava 1 (so `get` no secret)", n)
	}
	var objectos []string
	for _, linha := range strings.Split(rbac, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(linha), "#") {
			objectos = append(objectos, linha)
		}
	}
	for _, k := range []string{"ClusterRole", "cluster-admin", "\"*\""} {
		if strings.Contains(strings.Join(objectos, "\n"), k) {
			t.Errorf("tls-sync-rbac.yaml contem %q", k)
		}
	}
	if !strings.Contains(script, `SECRET="${SECRET:-aos-node-tls}"`) || !strings.Contains(script, `NS="${NS:-default}"`) {
		t.Error("o secret/namespace do sync-tls.sh divergiu do que o tls-sync-rbac.yaml autoriza")
	}
}

func aos446Contem(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
