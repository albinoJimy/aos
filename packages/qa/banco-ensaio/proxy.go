package bancoensaio

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// O PROXY EFÉMERO. Os modos `proxy` e `real` põem, entre o nó de ensaio e o fornecedor, a MESMA
// imagem do proxy que corre em produção, fixada pelo digest. O banco levanta-a num contentor
// Docker só para a corrida, e desmonta-a no fim.
//
// # ONDE ANDAM OS SEGREDOS
//
// A chave e a base da API do fornecedor chegam ao contentor do proxy pelo AMBIENTE do processo
// `docker` (`-e NOME`, sem valor no argumento): não aparecem na linha de comandos, nem no
// ficheiro de configuração do proxy — que as refere por `os.environ/NOME`. A chave mestra do
// proxy é aleatória, criada para a corrida, e viaja da mesma maneira.
//
// # O QUE O BANCO NÃO FAZ
//
// Não descarrega a imagem: se não estiver presente, falha e diz o comando. Não imprime os logs
// do proxy em bruto: o que mostra de um arranque falhado passa pelo [Redactor].

// ImagemDoProxy é a imagem de produção do proxy, pelo digest (a do AOS-505 e do AOS-508).
const ImagemDoProxy = "ghcr.io/berriai/litellm@sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63"

// Os nomes das variáveis de ambiente DENTRO do contentor do proxy.
const (
	envChaveMestra  = "LITELLM_MASTER_KEY"
	envChaveDaRota  = "ENSAIO_API_KEY"
	envBaseDaRota   = "ENSAIO_API_BASE"
	portaDoProxy    = "4000"
	portaDoFalso    = "8080"
	esperaPeloProxy = 4 * time.Minute
)

// PedidoDeProxy é o que o lançador precisa para levantar o proxy à frente de uma rota.
type PedidoDeProxy struct {
	// Prefixo é o adaptador do proxy: `openai` (a rota de produção) ou `anthropic`.
	Prefixo string
	// Modelo é o nome do modelo no fornecedor.
	Modelo string
	// BinarioDoFalso, quando não vazio, é o caminho de um binário LINUX do banco: o lançador
	// põe-no num contentor a fazer de fornecedor (modo `proxy`), e a rota aponta para ele.
	BinarioDoFalso string
	// Roteiro é o roteiro do provider falso.
	Roteiro []Comportamento

	apiKey  string
	apiBase string
}

// String não mostra os segredos.
func (p PedidoDeProxy) String() string { return "pedido de proxy [segredos ocultos]" }

// GoString idem.
func (p PedidoDeProxy) GoString() string { return p.String() }

// ComSegredos devolve o pedido com a chave e a base da API da rota.
func (p PedidoDeProxy) ComSegredos(apiKey, apiBase string) PedidoDeProxy {
	p.apiKey, p.apiBase = apiKey, apiBase
	return p
}

// ProxyVivo é um proxy efémero a correr.
type ProxyVivo struct {
	// BaseURL é a raiz da API do proxy (`http://127.0.0.1:<porta>/v1`).
	BaseURL string
	// Fechar desmonta o proxy. Chama-se sempre.
	Fechar func()

	chaveMestra string
}

// ChaveMestra é o `Bearer` que o nó de ensaio apresenta ao proxy. É um segredo da corrida.
func (p *ProxyVivo) ChaveMestra() string { return p.chaveMestra }

// NovoProxyVivo constrói um [ProxyVivo]; serve aos lançadores de teste.
func NovoProxyVivo(baseURL, chaveMestra string, fechar func()) *ProxyVivo {
	if fechar == nil {
		fechar = func() {}
	}
	return &ProxyVivo{BaseURL: baseURL, Fechar: fechar, chaveMestra: chaveMestra}
}

// LancadorDeProxy levanta o proxy efémero. A implementação de produção do banco é
// [LancadorDocker]; os testes injectam outra.
type LancadorDeProxy interface {
	Lancar(ctx context.Context, p PedidoDeProxy) (*ProxyVivo, error)
}

// Os erros do lançador.
var (
	// ErrSemDocker — não há Docker, o daemon não responde ou a imagem do proxy não está
	// descarregada.
	ErrSemDocker = errors.New("banco-ensaio: Docker ou a imagem do proxy nao estao disponiveis")
	// ErrProxy — o proxy não se conseguiu levantar.
	ErrProxy = errors.New("banco-ensaio: o proxy efemero nao arrancou")
)

// LancadorDocker levanta o proxy com o Docker local.
type LancadorDocker struct {
	// Redactor limpa os segredos de tudo o que o lançador mostra de um erro.
	Redactor *Redactor
}

// DockerDisponivel diz se há Docker e a imagem do proxy; o motivo, quando não.
func DockerDisponivel(ctx context.Context) (bool, string) {
	if _, err := exec.LookPath("docker"); err != nil {
		return false, "docker ausente do PATH"
	}
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		return false, "daemon docker inacessivel"
	}
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", ImagemDoProxy).Run(); err != nil {
		return false, "imagem do proxy nao descarregada (docker pull " + ImagemDoProxy + ")"
	}
	return true, ""
}

// docker corre um comando docker. `ambiente` são pares NOME=valor a acrescentar ao ambiente do
// processo docker — é por aí, e nunca pelos argumentos, que os segredos chegam ao contentor.
func (l *LancadorDocker) docker(ctx context.Context, ambiente []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- os argumentos sao do banco: nomes gerados, a imagem fixa e caminhos temporarios; nenhum vem do modelo
	cmd.Env = append(os.Environ(), ambiente...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Só o verbo do comando e a saída REDIGIDA: nunca os argumentos em bruto.
		return "", fmt.Errorf("docker %s: %s", args[0], l.Redactor.Texto(ultimasLinhas(string(out), 12)))
	}
	return strings.TrimSpace(string(out)), nil
}

func ultimasLinhas(s string, n int) string {
	linhas := strings.Split(strings.TrimSpace(s), "\n")
	if len(linhas) > n {
		linhas = linhas[len(linhas)-n:]
	}
	return strings.Join(linhas, " | ")
}

// arranqueDoProxy espera pela configuração (copiada para o contentor) e substitui-se pelo proxy.
const arranqueDoProxy = `
import os, time
while not os.path.exists("/tmp/config.pronto"):
    time.sleep(0.2)
os.execvp("litellm", ["litellm", "--config", "/tmp/config.yaml", "--port", "` + portaDoProxy + `"])
`

// arranqueDoFalso espera pelo binário do banco (copiado para o contentor) e substitui-se por ele.
const arranqueDoFalso = `
import os, sys, time
while not os.path.exists("/tmp/falso.pronto"):
    time.sleep(0.2)
os.chmod("/tmp/aos-ensaio", 0o755)
os.execv("/tmp/aos-ensaio", ["/tmp/aos-ensaio"] + sys.argv[1:])
`

// configDoProxy devolve a configuração do proxy para a rota. Os segredos NÃO entram: são
// referidos pelo nome da variável de ambiente.
func configDoProxy(prefixo, modelo string, comBase bool) (string, error) {
	if prefixo != "openai" && prefixo != "anthropic" {
		return "", fmt.Errorf("%w: adaptador do proxy desconhecido", ErrProxy)
	}
	if !nomeDeModeloAceite(modelo) {
		return "", fmt.Errorf("%w: nome de modelo com caracteres que o banco nao aceita", ErrProxy)
	}
	var b strings.Builder
	b.WriteString("model_list:\n  - model_name: " + AliasDaRota + "\n    litellm_params:\n")
	b.WriteString("      model: " + prefixo + "/" + modelo + "\n")
	b.WriteString("      api_key: os.environ/" + envChaveDaRota + "\n")
	if comBase {
		b.WriteString("      api_base: os.environ/" + envBaseDaRota + "\n")
	}
	// `drop_params: true` é o da configuração do proxy do nó (deploy/node/dev-hardened/litellm).
	// `num_retries: 0`: um pedido do banco é UM pedido ao fornecedor — sem isto o proxy podia
	// repetir por conta própria, e o tecto do dia deixava de contar o que sai.
	b.WriteString("litellm_settings:\n  drop_params: true\n  telemetry: false\n  num_retries: 0\n")
	b.WriteString("router_settings:\n  num_retries: 0\n")
	b.WriteString("general_settings:\n  background_health_checks: false\n")
	return b.String(), nil
}

// arranqueDoContentorDoProxy devolve os ARGUMENTOS do `docker run` do proxy e o AMBIENTE a
// acrescentar ao processo docker. Os segredos vão SÓ no ambiente: os argumentos levam os nomes
// das variáveis (`-e NOME`, sem valor), e por isso não aparecem na linha de comandos de
// processo nenhum.
func arranqueDoContentorDoProxy(contentor, rede, chaveMestra, apiKey, apiBase string) (args, ambiente []string) {
	ambiente = []string{envChaveMestra + "=" + chaveMestra, envChaveDaRota + "=" + apiKey}
	args = []string{"run", "-d", "--name", contentor, "--network", rede, "-p", "127.0.0.1::" + portaDoProxy,
		"-e", envChaveMestra, "-e", envChaveDaRota}
	if apiBase != "" {
		ambiente = append(ambiente, envBaseDaRota+"="+apiBase)
		args = append(args, "-e", envBaseDaRota)
	}
	return append(args, "--entrypoint", "python", ImagemDoProxy, "-c", arranqueDoProxy), ambiente
}

// Lancar implementa [LancadorDeProxy].
func (l *LancadorDocker) Lancar(ctx context.Context, p PedidoDeProxy) (*ProxyVivo, error) {
	if ok, motivo := DockerDisponivel(ctx); !ok {
		return nil, fmt.Errorf("%w: %s", ErrSemDocker, motivo)
	}
	aleatorio := make([]byte, 24)
	if _, err := rand.Read(aleatorio); err != nil {
		return nil, err
	}
	sufixo := hex.EncodeToString(aleatorio[:6])
	chaveMestra := "sk-ensaio-" + hex.EncodeToString(aleatorio[6:])
	l.Redactor.Acrescentar(chaveMestra, p.apiKey, p.apiBase)
	rede, falso, proxy := "aos512-rede-"+sufixo, "aos512-falso-"+sufixo, "aos512-proxy-"+sufixo

	// A limpeza corre com um contexto próprio: tem de acontecer mesmo que o da corrida tenha
	// sido cancelado.
	fechar := func() {
		limpeza, cancelar := context.WithTimeout(context.Background(), time.Minute)
		defer cancelar()
		_, _ = l.docker(limpeza, nil, "rm", "-f", proxy, falso)
		_, _ = l.docker(limpeza, nil, "network", "rm", rede)
	}
	falhar := func(err error) (*ProxyVivo, error) {
		fechar()
		return nil, fmt.Errorf("%w: %v", ErrProxy, err)
	}
	if _, err := l.docker(ctx, nil, "network", "create", rede); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProxy, err)
	}
	dir, err := os.MkdirTemp("", "aos-ensaio-proxy-")
	if err != nil {
		return falhar(err)
	}
	defer os.RemoveAll(dir)
	pronto := filepath.Join(dir, "pronto")
	if err := os.WriteFile(pronto, []byte("1"), 0o600); err != nil {
		return falhar(err)
	}

	apiBase := p.apiBase
	if p.BinarioDoFalso != "" {
		// O provider falso, num contentor da mesma imagem (só pelo sistema de ficheiros: o
		// binário é estático). Exige a chave da rota — pelo seu sha256, que não é segredo.
		soma := sha256.Sum256([]byte(p.apiKey))
		if _, err := l.docker(ctx, nil, "run", "-d", "--name", falso, "--network", rede, "--entrypoint", "python",
			ImagemDoProxy, "-c", arranqueDoFalso, "falso-provider", "--escuta", "0.0.0.0:"+portaDoFalso,
			"--roteiro", EscreverRoteiro(p.Roteiro), "--chave-sha256", hex.EncodeToString(soma[:])); err != nil {
			return falhar(err)
		}
		if _, err := l.docker(ctx, nil, "cp", p.BinarioDoFalso, falso+":/tmp/aos-ensaio"); err != nil {
			return falhar(err)
		}
		if _, err := l.docker(ctx, nil, "cp", pronto, falso+":/tmp/falso.pronto"); err != nil {
			return falhar(err)
		}
		apiBase = "http://" + falso + ":" + portaDoFalso + "/v1"
	}

	cfg, err := configDoProxy(p.Prefixo, p.Modelo, apiBase != "")
	if err != nil {
		return falhar(err)
	}
	caminhoDaCfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(caminhoDaCfg, []byte(cfg), 0o600); err != nil {
		return falhar(err)
	}
	args, ambiente := arranqueDoContentorDoProxy(proxy, rede, chaveMestra, p.apiKey, apiBase)
	if _, err := l.docker(ctx, ambiente, args...); err != nil {
		return falhar(err)
	}
	if _, err := l.docker(ctx, nil, "cp", caminhoDaCfg, proxy+":/tmp/config.yaml"); err != nil {
		return falhar(err)
	}
	if _, err := l.docker(ctx, nil, "cp", pronto, proxy+":/tmp/config.pronto"); err != nil {
		return falhar(err)
	}
	mapeada, err := l.docker(ctx, nil, "port", proxy, portaDoProxy+"/tcp")
	if err != nil {
		return falhar(err)
	}
	primeira := strings.TrimSpace(strings.Split(mapeada, "\n")[0])
	endereco := "http://127.0.0.1:" + primeira[strings.LastIndex(primeira, ":")+1:]

	cliente := &http.Client{Timeout: 2 * time.Second}
	limite := time.Now().Add(esperaPeloProxy)
	for {
		if resp, err := cliente.Get(endereco + "/health/liveliness"); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if ctx.Err() != nil || time.Now().After(limite) {
			logs, _ := l.docker(context.Background(), nil, "logs", "--tail", "30", proxy)
			return falhar(fmt.Errorf("o proxy nao ficou vivo a tempo; ultimas linhas (redigidas): %s", l.Redactor.Texto(ultimasLinhas(logs, 30))))
		}
		time.Sleep(time.Second)
	}
	return &ProxyVivo{BaseURL: endereco + "/v1", Fechar: fechar, chaveMestra: chaveMestra}, nil
}

// Redactor substitui os segredos conhecidos por `[oculto]` em tudo o que o banco escreve. É uma
// segunda linha: o banco não põe segredos nas suas mensagens, mas o que vem de fora — a saída
// do Docker, os logs do proxy, um erro de transporte com o endereço — passa por aqui antes de
// chegar a um ecrã ou a um ficheiro.
type Redactor struct {
	segredos []string
}

// Acrescentar regista valores a ocultar. Valores com menos de 6 bytes são ignorados: ocultá-los
// apagaria texto comum, e nenhum segredo real é tão curto.
func (r *Redactor) Acrescentar(valores ...string) {
	if r == nil {
		return
	}
	for _, v := range valores {
		if len(v) >= 6 {
			r.segredos = append(r.segredos, v)
		}
	}
}

// Texto devolve s com os segredos ocultados.
func (r *Redactor) Texto(s string) string {
	if r == nil {
		return s
	}
	for _, v := range r.segredos {
		s = strings.ReplaceAll(s, v, "[oculto]")
	}
	return s
}

// Escritor devolve um escritor que oculta os segredos do que passa para w. Cada Write é
// redigido inteiro: o banco escreve uma mensagem por chamada.
func (r *Redactor) Escritor(w io.Writer) io.Writer { return escritorRedigido{r: r, w: w} }

type escritorRedigido struct {
	r *Redactor
	w io.Writer
}

func (e escritorRedigido) Write(p []byte) (int, error) {
	if _, err := io.WriteString(e.w, e.r.Texto(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}
