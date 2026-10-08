package bancoensaio

// AOS-512 — O QUE A REVISÃO ADVERSARIAL PEDIU (REV-512, sobre o commit 6fbf2173).
//
// I1 proxy órfão · I2 destino da chave · I3 dois processos no contador · I4 tecto contornável ·
// m2 chave recusada · m3 aspas · m4 guarda da porta pública por AST · e as duas mutações que
// tinham sobrevivido (erro de RegistarUso engolido; marcador do exemplo só com `<` ou só com `>`).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------
// O processo ajudante: os testes que precisam de um SEGUNDO processo relançam o próprio
// binário de teste com `AOS512_AJUDANTE` definido.
// ---------------------------------------------------------------------------------------

const envDoAjudante = "AOS512_AJUDANTE"

// TestAOS512_AjudanteDeProcesso não é um teste: é o corpo do processo ajudante. Sem a variável
// de ambiente não faz nada.
func TestAOS512_AjudanteDeProcesso(t *testing.T) {
	switch os.Getenv(envDoAjudante) {
	case "":
		return
	case "contador":
		// Abre o contador, avisa e fica à espera de ser morto — sem o fechar.
		c, err := AbrirContador(os.Getenv("AOS512_CONTADOR"), FornecedorKimi, Tectos{PedidosDia: 100}, nil, "m", nil)
		if err != nil {
			fmt.Println("ERRO " + err.Error())
			return
		}
		_ = c
		fmt.Println("ABERTO")
		time.Sleep(10 * time.Minute)
	case "proxy":
		// Levanta o proxy com o Docker, avisa e fica à espera de ser morto — sem o desmontar.
		tolerancia, _ := time.ParseDuration(os.Getenv("AOS512_TOLERANCIA"))
		l := &LancadorDocker{Redactor: &Redactor{}, Intervalo: 2 * time.Second, Tolerancia: tolerancia}
		vivo, err := l.Lancar(context.Background(), PedidoDeProxy{
			Prefixo: "openai", Modelo: "modelo-falso-do-banco", BinarioDoFalso: os.Getenv("AOS_BANCO_FALSO_BIN"),
			Roteiro: []Comportamento{ComportamentoCumpre}, Vida: 20 * time.Minute,
		}.ComSegredos("sk-falso-do-ajudante-0123456789", ""))
		if err != nil {
			fmt.Println("ERRO " + err.Error())
			return
		}
		_ = vivo
		fmt.Println("LANCADO")
		time.Sleep(20 * time.Minute)
	}
}

// lancarAjudante relança o binário de teste como ajudante e espera pela sua primeira linha.
func lancarAjudante(t *testing.T, papel string, ambiente ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAOS512_AjudanteDeProcesso$", "-test.timeout=30m")
	cmd.Env = append(append(os.Environ(), envDoAjudante+"="+papel), ambiente...)
	saida, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	linhas := make(chan string, 1)
	go func() {
		leitor := bufio.NewScanner(saida)
		for leitor.Scan() {
			if l := leitor.Text(); l == "ABERTO" || l == "LANCADO" || strings.HasPrefix(l, "ERRO ") {
				linhas <- l
				return
			}
		}
		linhas <- "ERRO o ajudante saiu sem dizer nada"
	}()
	select {
	case l := <-linhas:
		if strings.HasPrefix(l, "ERRO ") {
			t.Fatalf("ajudante %s: %s", papel, l)
		}
	case <-time.After(8 * time.Minute):
		t.Fatalf("ajudante %s: nao arrancou a tempo", papel)
	}
	return cmd
}

// matar mata o ajudante À FORÇA: sem sinais, sem `defer`, sem limpeza.
func matar(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
}

// ---------------------------------------------------------------------------------------
// I3 — dois processos sobre o mesmo contador
// ---------------------------------------------------------------------------------------

// Um segundo Contador sobre o mesmo ficheiro recusa abrir enquanto o primeiro não fechar, e um
// contador fechado não reserva.
func TestAOS512_Contador_ExclusaoNoMesmoProcesso(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "contador.json")
	primeiro, err := AbrirContador(caminho, FornecedorKimi, Tectos{PedidosDia: 10}, nil, "m", relogioFixo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AbrirContador(caminho, FornecedorKimi, Tectos{PedidosDia: 10}, nil, "m", relogioFixo); !errors.Is(err, ErrContadorEmUso) {
		t.Fatalf("segundo contador sobre o mesmo ficheiro: err = %v, quer ErrContadorEmUso", err)
	}
	if err := primeiro.Reservar(); err != nil {
		t.Fatalf("o primeiro continua a servir: %v", err)
	}
	primeiro.Fechar()
	if err := primeiro.Reservar(); !errors.Is(err, ErrContador) {
		t.Fatalf("um contador fechado nao pode reservar: %v", err)
	}
	if _, err := os.Stat(caminho + sufixoDaTrava); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Fechar tinha de remover o ficheiro de exclusao")
	}
	segundo, err := AbrirContador(caminho, FornecedorKimi, Tectos{PedidosDia: 10}, nil, "m", relogioFixo)
	if err != nil {
		t.Fatalf("depois de fechado, o contador abre: %v", err)
	}
	defer segundo.Fechar()
	if e, _ := segundo.Estado(); e.PedidosHoje != 1 {
		t.Errorf("o segundo tinha de ver o pedido do primeiro: %d", e.PedidosHoje)
	}
	// Os temporários têm nome único e não ficam para trás.
	if sobras, _ := filepath.Glob(caminho + ".*.tmp"); len(sobras) != 0 {
		t.Errorf("ficaram temporarios do contador: %v", sobras)
	}
	if antigo, _ := filepath.Glob(caminho + ".tmp"); len(antigo) != 0 {
		t.Errorf("o temporario de nome fixo voltou: %v", antigo)
	}
}

// DOIS PROCESSOS. Com o contador aberto por outro processo, este recusa arrancar — pela
// biblioteca e pela linha de comandos, com causa própria. Morto esse processo à força, a trava
// fica: não se remove sozinha. Só o `limpar` a remove, e só porque o processo já não existe.
func TestAOS512_Contador_DoisProcessos(t *testing.T) {
	chaves := escreverChaves(t, chavesDeTeste())
	caminho := filepath.Join(filepath.Dir(chaves), "contador.json")
	ajudante := lancarAjudante(t, "contador", "AOS512_CONTADOR="+caminho)

	abrir := func() error {
		c, err := AbrirContador(caminho, FornecedorKimi, Tectos{PedidosDia: 100}, nil, "m", relogioFixo)
		if err == nil {
			c.Fechar()
		}
		return err
	}
	if err := abrir(); !errors.Is(err, ErrContadorEmUso) || !strings.Contains(err.Error(), strconv.Itoa(ajudante.Process.Pid)) {
		t.Fatalf("com o contador aberto por outro processo: err = %v, quer ErrContadorEmUso com o PID %d", err, ajudante.Process.Pid)
	}
	lanc := &lancadorDeTeste{t: t}
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--saida", t.TempDir())
	if e.codigo != SaidaRecusada || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "em uso por outro processo") {
		t.Fatalf("a corrida real com o contador em uso: codigo %d, lancamentos %d: %q", e.codigo, lanc.lancamentos, e.stderr)
	}
	// O `limpar` NÃO remove a trava de um processo vivo.
	if removida, err := RemoverTravaMorta(caminho); removida || err == nil || !strings.Contains(err.Error(), "ainda existe") {
		t.Fatalf("trava de um processo vivo: removida=%v err=%v", removida, err)
	}

	matar(t, ajudante)
	if err := abrir(); !errors.Is(err, ErrContadorEmUso) {
		t.Fatalf("a trava de um processo morto nao se remove sozinha: err = %v", err)
	}
	e = executar(t, Ambiente{}, "limpar", "--chaves", chaves)
	if !strings.Contains(e.stdout, "removida a trava do contador") {
		t.Fatalf("o limpar tinha de remover a trava do processo morto (codigo %d): %q %q", e.codigo, e.stdout, e.stderr)
	}
	if err := abrir(); err != nil {
		t.Fatalf("depois do limpar, o contador abre: %v", err)
	}
	if e := executar(t, Ambiente{}, "limpar", "--chaves", chaves); !strings.Contains(e.stdout, "nao tem trava") {
		t.Errorf("sem trava, o limpar di-lo: %q", e.stdout)
	}
	// Uma trava que não se lê não é removida: quem decide é uma pessoa.
	if err := os.WriteFile(caminho+sufixoDaTrava, []byte("lixo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if removida, err := RemoverTravaMorta(caminho); removida || err == nil {
		t.Errorf("trava ilegivel: removida=%v err=%v", removida, err)
	}
}

// ---------------------------------------------------------------------------------------
// I4, m3 — o ficheiro de chaves e o contador não deixam contornar o tecto
// ---------------------------------------------------------------------------------------

func TestAOS512_Chaves_ValoresQueNaoSaoUmTecto(t *testing.T) {
	com := func(campo, valor string) map[string]string {
		c := chavesDeTeste()
		c[campo] = valor
		return c
	}
	const valorMau = "SENTINELA-VALOR-MAU-77c0"
	for _, caso := range []struct {
		nome       string
		campos     map[string]string
		fornecedor string
		nomeia     string
		diz        string
	}{
		{"tecto absurdo", com(CampoTectoPedidosKimi, "100001"), "kimi", CampoTectoPedidosKimi, "acima do maximo"},
		{"tecto que nao cabe em 64 bits", com(CampoTectoPedidosKimi, "99999999999999999999999"), "kimi", CampoTectoPedidosKimi, "acima do maximo"},
		{"tecto com sinal de mais", com(CampoTectoPedidosKimi, "+500"), "kimi", CampoTectoPedidosKimi, "so algarismos"},
		{"tecto com espaco interior", com(CampoTectoPedidosKimi, "5 00"), "kimi", CampoTectoPedidosKimi, "so algarismos"},
		{"tecto em notacao cientifica", com(CampoTectoPedidosKimi, "1e3"), "kimi", CampoTectoPedidosKimi, "so algarismos"},
		{"tecto com comentario na linha", com(CampoTectoPedidosKimi, "500 # nota"), "kimi", CampoTectoPedidosKimi, "so algarismos"},
		{"tecto entre aspas", com(CampoTectoPedidosKimi, `"500"`), "kimi", CampoTectoPedidosKimi, "entre aspas"},
		{"chave entre aspas", com(CampoChaveKimi, `"`+valorMau+`"`), "kimi", CampoChaveKimi, "entre aspas"},
		{"chave entre plicas", com(CampoChaveKimi, `'`+valorMau+`'`), "kimi", CampoChaveKimi, "entre aspas"},
		{"base entre aspas", com(CampoKimiAPIBase, `"https://api.kimi.com/coding/v1"`), "kimi", CampoKimiAPIBase, "entre aspas"},
		{"tecto em dolares absurdo", com(CampoTectoUSDAnthropic, "1000.01"), "anthropic", CampoTectoUSDAnthropic, "acima do maximo"},
		{"tecto em dolares com sinal", com(CampoTectoUSDAnthropic, "+5"), "anthropic", CampoTectoUSDAnthropic, "ilegivel"},
		{"tecto em dolares entre aspas", com(CampoTectoUSDAnthropic, `"5"`), "anthropic", CampoTectoUSDAnthropic, "entre aspas"},
		// O marcador do exemplo é recusado inteiro, só com `<` e só com `>`.
		{"marcador so com o sinal de menor", com(CampoChaveKimi, "<"+valorMau), "kimi", CampoChaveKimi, "marcador do exemplo"},
		{"marcador so com o sinal de maior", com(CampoChaveKimi, valorMau+">"), "kimi", CampoChaveKimi, "marcador do exemplo"},
		{"modelo so com o sinal de maior", com(CampoAnthropicModelo, "por definir>"), "anthropic", CampoAnthropicModelo, "marcador do exemplo"},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			lanc := &lancadorDeTeste{t: t}
			e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", escreverChaves(t, caso.campos), "--fornecedor", caso.fornecedor, "--saida", t.TempDir())
			if e.codigo != SaidaRecusada || lanc.lancamentos != 0 {
				t.Fatalf("codigo %d (quer %d), lancamentos %d\n%s", e.codigo, SaidaRecusada, lanc.lancamentos, e.stderr)
			}
			if !strings.Contains(e.stderr, caso.nomeia) || !strings.Contains(e.stderr, caso.diz) {
				t.Errorf("a mensagem tinha de nomear %s e dizer %q: %q", caso.nomeia, caso.diz, e.stderr)
			}
			verSemFugas(t, caso.nome, e.stdout+e.stderr, []string{valorMau, sentinelaChaveKimi, sentinelaCaminhoDaBase})
		})
	}
	// O máximo, à justa, e os espaços À VOLTA do valor são aceites.
	for _, valor := range []string{"100000", " 500 ", "1"} {
		lanc := &lancadorDeTeste{t: t}
		if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", escreverChaves(t, com(CampoTectoPedidosKimi, valor)), "--fornecedor", "kimi", "--so-plano"); e.codigo != SaidaOK && !strings.Contains(e.stderr, "nao cabe") {
			t.Errorf("tecto %q tinha de ser aceite: codigo %d: %q", valor, e.codigo, e.stderr)
		}
	}
}

// Um campo repetido não é «ganha o último»: é erro, com o nome do campo e os números das linhas.
func TestAOS512_Chaves_CampoRepetido(t *testing.T) {
	caminho := escreverChaves(t, chavesDeTeste())
	cru, _ := os.ReadFile(caminho)
	linhas := strings.Split(string(cru), "\n")
	primeira := 0
	for i, l := range linhas {
		if strings.HasPrefix(l, CampoTectoPedidosKimi+"=") {
			primeira = i + 1
		}
	}
	if err := os.WriteFile(caminho, append(cru, []byte(CampoTectoPedidosKimi+"=99999\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	ultima := len(strings.Split(strings.TrimRight(string(cru), "\n"), "\n")) + 1
	lanc := &lancadorDeTeste{t: t}
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", caminho, "--fornecedor", "kimi", "--so-plano")
	quer := fmt.Sprintf("o campo %s esta repetido (linhas %d e %d)", CampoTectoPedidosKimi, primeira, ultima)
	if e.codigo != SaidaRecusada || lanc.lancamentos != 0 || !strings.Contains(e.stderr, quer) {
		t.Fatalf("campo repetido: codigo %d; quer a mensagem %q; veio %q", e.codigo, quer, e.stderr)
	}
	if strings.Contains(e.stderr, "99999") || strings.Contains(e.stderr, "=1000") {
		t.Errorf("a mensagem do campo repetido leva um valor: %q", e.stderr)
	}
}

// No modo real o contador é o que está ao lado do ficheiro de chaves: não há flag que o mude.
// E um contador desaparecido, havendo relatórios de corridas reais de hoje, não recomeça do zero.
func TestAOS512_Real_ContadorNaoSeContorna(t *testing.T) {
	chaves := escreverChaves(t, chavesDeTeste())
	pasta := filepath.Dir(chaves)
	contador := filepath.Join(pasta, "contador.json")

	lanc := &lancadorDeTeste{t: t}
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--contador", filepath.Join(t.TempDir(), "outro.json"))
	if e.codigo != SaidaUso || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "--contador") {
		t.Fatalf("--contador no modo real: codigo %d (quer %d): %q", e.codigo, SaidaUso, e.stderr)
	}

	// Uma corrida real: 12 pedidos e 1 de sonda, relatório em `relatorios/`, contador em 13.
	lanc = &lancadorDeTeste{t: t, roteiro: []Comportamento{ComportamentoCumpre}}
	if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--silencioso"); e.codigo != SaidaOK {
		t.Fatalf("a primeira corrida: codigo %d\n%s", e.codigo, e.stderr)
	}
	if lerContador(t, contador)[diaDosTestes]["kimi"].Pedidos != 13 {
		t.Fatalf("contador depois da primeira corrida: %v", lerContador(t, contador))
	}
	// O contador desaparece. A corrida seguinte é RECUSADA: há um relatório de hoje.
	if err := os.Remove(contador); err != nil {
		t.Fatal(err)
	}
	lanc = &lancadorDeTeste{t: t}
	e = executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--so-plano")
	if e.codigo != SaidaRecusada || !strings.Contains(e.stderr, "--reconstruir-contador") {
		t.Fatalf("contador desaparecido com relatorios de hoje: codigo %d (quer %d): %q", e.codigo, SaidaRecusada, e.stderr)
	}
	if _, err := os.Stat(contador); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("uma corrida recusada nao pode recriar o contador a zero")
	}
	// Com a flag explícita, reconstrói-se dos relatórios: os 13 pedidos voltam a contar.
	e = executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--so-plano", "--reconstruir-contador")
	if e.codigo != SaidaOK || !strings.Contains(e.stderr, "RECONSTRUIDO") || !strings.Contains(e.stderr, "restam 987 de 1000") {
		t.Fatalf("reconstrucao: codigo %d: %q", e.codigo, e.stderr)
	}
	if got := lerContador(t, contador)[diaDosTestes]["kimi"].Pedidos; got != 13 {
		t.Fatalf("contador reconstruido: %d pedidos, quer 13", got)
	}
	// Um relatório de OUTRO dia ou de outro fornecedor não conta, e sem relatórios de hoje um
	// contador ausente é o primeiro uso.
	if p, _, n := UsoDosRelatoriosDeHoje([]string{filepath.Join(pasta, "relatorios")}, FornecedorKimi, "2026-10-09"); p != 0 || n != 0 {
		t.Errorf("relatorios de outro dia contaram: %d pedidos em %d", p, n)
	}
	if p, _, n := UsoDosRelatoriosDeHoje([]string{filepath.Join(pasta, "relatorios")}, FornecedorAnthropic, diaDosTestes); p != 0 || n != 0 {
		t.Errorf("relatorios de outro fornecedor contaram: %d pedidos em %d", p, n)
	}
	if err := ReconstruirContador(contador, FornecedorKimi, diaDosTestes, 0, 0); !errors.Is(err, ErrContador) {
		t.Errorf("reconstruir um contador que existe: err = %v, quer ErrContador", err)
	}
}

// ---------------------------------------------------------------------------------------
// I2 — o destino da chave
// ---------------------------------------------------------------------------------------

func TestAOS512_Real_DestinoDaChave(t *testing.T) {
	com := func(base string) string {
		c := chavesDeTeste()
		c[CampoKimiAPIBase] = base
		return escreverChaves(t, c)
	}
	const hostMau = "sentinela-host-do-atacante.example"
	// Recusados: sem https, com utilizador, porta, query ou fragmento, e hosts fora da lista.
	for _, caso := range []struct{ nome, base, diz string }{
		{"http", "http://api.kimi.com/coding/v1", "sem https"},
		{"ftp com utilizador", "ftp://u:p@" + hostMau + "/coisa", "sem https"},
		{"nao e um URL", "nao-e-url", "sem https"},
		{"so o host", "api.kimi.com/coding/v1", "sem https"},
		{"utilizador no URL", "https://u:p@api.kimi.com/coding/v1", "com utilizador"},
		{"porta", "https://api.kimi.com:8443/coding/v1", "com utilizador"},
		{"query", "https://api.kimi.com/coding/v1?x=1", "com utilizador"},
		{"fragmento", "https://api.kimi.com/coding/v1#x", "com utilizador"},
		{"host fora da lista", "https://" + hostMau + "/v1", "nao e do fornecedor"},
		{"host parecido", "https://api.kimi.com.evil.example/v1", "nao e do fornecedor"},
		{"subdominio que nao esta na lista", "https://x.api.kimi.com/v1", "nao e do fornecedor"},
		{"host de outro fornecedor", "https://api.exemplo-de-outro-fornecedor.com/v1", "nao e do fornecedor"},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			lanc := &lancadorDeTeste{t: t}
			for _, extra := range [][]string{{"--so-plano"}, nil} {
				args := append([]string{"real", "--chaves", com(caso.base), "--fornecedor", "kimi", "--saida", t.TempDir()}, extra...)
				e := executar(t, Ambiente{Lancador: lanc}, args...)
				if e.codigo != SaidaRecusada || lanc.lancamentos != 0 {
					t.Fatalf("%v: codigo %d (quer %d), lancamentos %d: %q", extra, e.codigo, SaidaRecusada, lanc.lancamentos, e.stderr)
				}
				if !strings.Contains(e.stderr, CampoKimiAPIBase) || !strings.Contains(e.stderr, caso.diz) {
					t.Errorf("a mensagem tinha de nomear o campo e dizer %q: %q", caso.diz, e.stderr)
				}
				// O valor recusado não é ecoado: pode ser um engano com uma credencial dentro.
				verSemFugas(t, caso.nome, e.stdout+e.stderr, []string{hostMau, "evil.example", "u:p@", "DESTINO DA CHAVE"})
			}
		})
	}

	// Aceites: os hosts da lista, e o --so-plano MOSTRA o destino (esquema e host, sem caminho).
	for _, host := range HostsDoFornecedor(FornecedorKimi) {
		lanc := &lancadorDeTeste{t: t}
		e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", com("https://"+host+"/"+sentinelaCaminhoDaBase+"/v1"), "--fornecedor", "kimi", "--so-plano")
		if e.codigo != SaidaOK || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "DESTINO DA CHAVE: https://"+host+"  ") {
			t.Errorf("host %s: codigo %d: %q", host, e.codigo, e.stderr)
		}
		verSemFugas(t, "destino mostrado", e.stderr, []string{sentinelaCaminhoDaBase})
	}
	// O início da corrida também o mostra, antes de lançar o proxy.
	lanc := &lancadorDeTeste{t: t, roteiro: []Comportamento{ComportamentoCumpre}}
	if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", escreverChaves(t, chavesDeTeste()), "--fornecedor", "kimi", "--silencioso"); e.codigo != SaidaOK || !strings.Contains(e.stderr, "DESTINO DA CHAVE: https://api.kimi.com  ") {
		t.Errorf("a corrida tinha de mostrar o destino: codigo %d: %q", e.codigo, e.stderr)
	}

	// Fora da lista só com a flag, e a flag tem de REPETIR o host exacto. Continua a exigir https.
	fora := com("https://" + hostMau + "/v1")
	lanc = &lancadorDeTeste{t: t}
	for nome, flag := range map[string]string{"outro host": "outro.example", "so parte do host": "example", "com esquema": "https://" + hostMau} {
		if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", fora, "--fornecedor", "kimi", "--so-plano", "--destino-fora-da-lista", flag); e.codigo != SaidaRecusada {
			t.Errorf("--destino-fora-da-lista com %s: codigo %d, quer %d", nome, e.codigo, SaidaRecusada)
		}
	}
	if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", fora, "--fornecedor", "kimi", "--so-plano", "--destino-fora-da-lista", hostMau); e.codigo != SaidaOK || !strings.Contains(e.stderr, "DESTINO DA CHAVE: https://"+hostMau+"  ") {
		t.Errorf("com o host exacto na flag tinha de aceitar e mostrar o destino: codigo %d: %q", e.codigo, e.stderr)
	}
	if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", com("http://"+hostMau+"/v1"), "--fornecedor", "kimi", "--so-plano", "--destino-fora-da-lista", hostMau); e.codigo != SaidaRecusada || !strings.Contains(e.stderr, "sem https") {
		t.Errorf("a flag nao dispensa o https: codigo %d: %q", e.codigo, e.stderr)
	}
	// A flag com um destino que já é do fornecedor é um engano: recusa.
	if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", escreverChaves(t, chavesDeTeste()), "--fornecedor", "kimi", "--so-plano", "--destino-fora-da-lista", "api.kimi.com"); e.codigo != SaidaRecusada {
		t.Errorf("a flag com um host da lista: codigo %d, quer %d", e.codigo, SaidaRecusada)
	}
	// A Anthropic não tem base no ficheiro: o destino é o do adaptador do proxy, e o banco di-lo.
	if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", escreverChaves(t, func() map[string]string {
		c := chavesDeTeste()
		delete(c, CampoTectoUSDAnthropic)
		return c
	}()), "--fornecedor", "anthropic", "--so-plano"); e.codigo != SaidaOK || !strings.Contains(e.stderr, "DESTINO DA CHAVE: "+DestinoDaAnthropic+"  ") {
		t.Errorf("anthropic: codigo %d: %q", e.codigo, e.stderr)
	}
	if lanc.lancamentos != 0 {
		t.Errorf("nada disto podia lancar o proxy")
	}

	// O MECANISMO SÓ DE TESTE: uma base http aceite sem validação, que só o campo não
	// exportado do Ambiente liga. Não há flag: a linha de comandos não o alcança.
	deTeste := "http://127.0.0.1:1/v1"
	lanc = &lancadorDeTeste{t: t}
	if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", com(deTeste), "--fornecedor", "kimi", "--so-plano"); e.codigo != SaidaRecusada {
		t.Errorf("sem o mecanismo de teste, uma base http e recusada: codigo %d", e.codigo)
	}
	if e := executar(t, Ambiente{Lancador: lanc, destinoDeTeste: deTeste}, "real", "--chaves", com(deTeste), "--fornecedor", "kimi", "--so-plano"); e.codigo != SaidaOK || !strings.Contains(e.stderr, "DESTINO DA CHAVE: http://127.0.0.1:1  ") {
		t.Errorf("com o mecanismo de teste: codigo %d: %q", e.codigo, e.stderr)
	}
	if e := executar(t, Ambiente{Lancador: lanc, destinoDeTeste: deTeste}, "real", "--chaves", com("http://127.0.0.1:2/v1"), "--fornecedor", "kimi", "--so-plano"); e.codigo != SaidaRecusada {
		t.Errorf("o mecanismo de teste so aceita a base exacta: codigo %d", e.codigo)
	}
}

// ---------------------------------------------------------------------------------------
// As mutações que tinham sobrevivido à revisão
// ---------------------------------------------------------------------------------------

// O erro de RegistarUso não é engolido: se o gasto estimado de uma resposta não se consegue
// gravar, o turno falha e o run acaba como `contador_inutilizavel`.
func TestAOS512_Tecto_ErroAoRegistarOUsoNaoEEngolido(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "contador.json")
	precos := &TabelaDePrecos{Modelos: map[string]Preco{"m": {EntradaMicroUSDPorMTok: 1, SaidaMicroUSDPorMTok: 1}}}
	contador, err := AbrirContador(caminho, FornecedorAnthropic, Tectos{PedidosDia: 100, MicroUSDDia: 5_000_000, TemUSD: true}, precos, "m", relogioFixo)
	if err != nil {
		t.Fatal(err)
	}
	defer contador.Fechar()
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	falso := NovoProviderFalso([]Comportamento{ComportamentoCumpre})
	// O contador estraga-se DEPOIS de o pedido ter sido reservado e ANTES de a resposta chegar:
	// a reserva passou, e é o registo do uso que encontra o ficheiro ilegível.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if werr := os.WriteFile(caminho, []byte("{estragado a meio do pedido"), 0o600); werr != nil {
			t.Error(werr)
		}
		falso.ServeHTTP(w, r)
	}))
	defer srv.Close()
	no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{Bateria: b, BaseURL: srv.URL + "/v1", Credencial: "c", Contador: contador})
	if err != nil {
		t.Fatal(err)
	}
	defer no.Fechar()
	caso, _ := b.Caso("T3")
	doc, _ := b.Documento(caso.Nos[0].EntradaDoc)
	o, saida := no.Correr(context.Background(), PedidoDeRun{Caso: caso, No: caso.Nos[0], Braco: BracoA, Tentativa: 1, Entrada: doc, OrigemEntrada: "bateria", MaxTurnos: 1})
	if falso.Pedidos() != 1 {
		t.Fatalf("o pedido tinha de ter saido (a reserva passou): %d", falso.Pedidos())
	}
	if o.Desfecho != DesfechoContador || saida != "" {
		t.Fatalf("desfecho = %s, quer %s: o erro de registar o uso foi engolido", o.Desfecho, DesfechoContador)
	}
}

// A CHAVE RECUSADA: três 401 ou 403 seguidos desde o primeiro pedido abortam a corrida real.
// Um erro que não é de autenticação, ou uma recusa depois de um pedido bom, não aborta.
func TestAOS512_Corrida_ChaveRecusadaAborta(t *testing.T) {
	correr := func(codigos []int, abortar int) (*Relatorio, int) {
		b, err := CarregarBateria()
		if err != nil {
			t.Fatal(err)
		}
		falso := NovoProviderFalso([]Comportamento{ComportamentoCumpre})
		n := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			codigo := http.StatusOK
			if n < len(codigos) {
				codigo = codigos[n]
			}
			n++
			if codigo != http.StatusOK {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(codigo)
				fmt.Fprint(w, `{"error":{"message":"recusado"}}`)
				return
			}
			falso.ServeHTTP(w, r)
		}))
		defer srv.Close()
		no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{Bateria: b, BaseURL: srv.URL + "/v1", Credencial: "c"})
		if err != nil {
			t.Fatal(err)
		}
		defer no.Fechar()
		r, err := Correr(context.Background(), CfgDaCorrida{Modo: ModoFalso, Plano: planoDeUmPedido(b, 6), Bateria: b, No: no, Relogio: relogioFixo, AbortarAposRecusasDeChave: abortar})
		if err != nil {
			t.Fatal(err)
		}
		return r, n
	}
	if r, n := correr([]int{401, 403, 401, 401, 401, 401}, 3); r.Terminou != TerminouChaveRecusada || n != 3 {
		t.Errorf("tres recusas de chave seguidas: terminou=%s com %d pedidos; quer chave_recusada e 3", r.Terminou, n)
	}
	if r, n := correr([]int{401, 401, 200, 401, 401, 401}, 3); r.Terminou != TerminouCompleta || n != 6 {
		t.Errorf("um pedido bom entre as recusas: terminou=%s com %d pedidos; quer completa e 6", r.Terminou, n)
	}
	if r, n := correr([]int{500, 500, 500, 500, 500, 500}, 3); r.Terminou != TerminouCompleta || n != 6 {
		t.Errorf("erros que nao sao de autenticacao: terminou=%s com %d pedidos; quer completa e 6", r.Terminou, n)
	}
	if r, n := correr([]int{401, 401, 401, 401, 401, 401}, 0); r.Terminou != TerminouCompleta || n != 6 {
		t.Errorf("sem a regra ligada: terminou=%s com %d pedidos; quer completa e 6", r.Terminou, n)
	}
}

// Os 429 contam-se à parte de «erro do provider», e os limites dos modos com proxy dizem que a
// configuração do proxy do ensaio não é a de produção.
func TestAOS512_Taxas_429APartEELimitesDoProxy(t *testing.T) {
	obs := []Observacao{
		{Caso: "T1", No: "ler", Braco: "A", Tentativa: 1, HTTP: []int{200, 429}},
		{Caso: "T1", No: "ler", Braco: "A", Amostra: 1, Tentativa: 1, HTTP: []int{401, 429, 0}},
	}
	tx := CalcularTaxas(obs)
	verTaxa(t, "erro do provider", tx.ErroDoProvider, 2, 5)
	verTaxa(t, "limite de taxa", tx.LimiteDeTaxa429, 2, 5)
	for modo, quer := range map[string]bool{ModoFalso: false, ModoProxy: true, ModoReal: true} {
		limites := strings.Join(limitesDaCorrida(CfgDaCorrida{Modo: modo, Plano: Plano{Experiencia: ExperienciaBateria}}), "\n")
		tem := strings.Contains(limites, "num_retries: 0") && strings.Contains(limites, "drop_params: true") &&
			strings.Contains(limites, "disable_cooldowns: true") && strings.Contains(limites, "429")
		if tem != quer {
			t.Errorf("modo %s: os limites sobre a configuracao do proxy e os 429 — tem=%v, quer=%v", modo, tem, quer)
		}
	}
	separadores := strings.Join(limitesDaCorrida(CfgDaCorrida{Modo: ModoReal, Plano: PlanoDosSeparadores(1)}), "\n")
	if !strings.Contains(separadores, "Fisher") || !strings.Contains(separadores, "Holm") || !strings.Contains(separadores, "tres comparacoes") {
		t.Errorf("os limites dos separadores tinham de dizer o teste, a correccao e o numero de comparacoes")
	}
}

// ---------------------------------------------------------------------------------------
// m4 — a porta pública dos providers falsos, verificada pelos imports (AST), não por texto
// ---------------------------------------------------------------------------------------

// importaAPorta diz se um ficheiro Go importa a porta pública dos providers falsos ou o banco,
// lendo os imports com o parser — qualquer que seja a forma de os escrever (aspas, acentos
// graves, alias, import em branco).
func importaAPorta(caminho string) (bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), caminho, nil, parser.ImportsOnly)
	if err != nil {
		return false, err
	}
	for _, imp := range f.Imports {
		alvo, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil {
			return false, uerr
		}
		if alvo == "github.com/aos-ref/platform/model-gateway/wiretest" || strings.HasPrefix(alvo, "github.com/aos-ref/qa/banco-ensaio") {
			return true, nil
		}
	}
	return false, nil
}

func TestAOS512_NinguemForaDoBancoImportaAPortaDosFalsos(t *testing.T) {
	raiz := raizDosPacotes(t)
	lidos := 0
	err := filepath.WalkDir(raiz, func(caminho string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(caminho, raiz))
		if d.IsDir() {
			if rel == "/qa/banco-ensaio" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		importa, perr := importaAPorta(caminho)
		if perr != nil {
			return nil // um ficheiro que não é Go válido (casos de teste de linters) não importa nada
		}
		lidos++
		if importa {
			t.Errorf("%s importa a porta publica dos providers falsos ou o banco — so o banco o pode fazer", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if lidos < 1500 {
		t.Fatalf("so foram lidos %d ficheiros Go: o teste nao esta a varrer packages/", lidos)
	}
	// A verificação apanha TODAS as grafias de um import — incluindo a que a guarda textual
	// deixava passar (o caminho entre acentos graves, num import em branco).
	dir := t.TempDir()
	for nome, fonte := range map[string]string{
		"aspas.go":   "package x\n\nimport \"github.com/aos-ref/platform/model-gateway/wiretest\"\n",
		"graves.go":  "package x\n\nimport _ `github.com/aos-ref/platform/model-gateway/wiretest`\n",
		"alias.go":   "package x\n\nimport (\n\tw \"github.com/aos-ref/platform/model-gateway/wiretest\"\n)\n",
		"o-banco.go": "package x\n\nimport _ \"github.com/aos-ref/qa/banco-ensaio\"\n",
	} {
		caminho := filepath.Join(dir, nome)
		if err := os.WriteFile(caminho, []byte(fonte), 0o600); err != nil {
			t.Fatal(err)
		}
		if importa, err := importaAPorta(caminho); err != nil || !importa {
			t.Errorf("%s: a verificacao nao apanhou o import (err=%v)", nome, err)
		}
	}
	inocente := filepath.Join(dir, "inocente.go")
	if err := os.WriteFile(inocente, []byte("package x\n\n// fala de github.com/aos-ref/platform/model-gateway/wiretest num comentario\nimport \"github.com/aos-ref/platform/model-gateway/port\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if importa, err := importaAPorta(inocente); err != nil || importa {
		t.Errorf("um comentario nao e um import (err=%v)", err)
	}
}

// ---------------------------------------------------------------------------------------
// I1 — o proxy órfão (com Docker; salta declaradamente sem ele)
// ---------------------------------------------------------------------------------------

// contentoresDoBanco devolve os contentores `aos512-*` que existem.
func contentoresDoBanco(t *testing.T) []string {
	t.Helper()
	nomes, err := nomesDoDocker(context.Background(), "ps", "-a", "--filter", "name="+prefixoDoDocker, "--format", "{{.Names}}")
	if err != nil {
		t.Fatal(err)
	}
	return nomes
}

func temProxy(nomes []string) bool {
	for _, n := range nomes {
		if strings.HasPrefix(n, prefixoDoDocker+"proxy-") {
			return true
		}
	}
	return false
}

// O processo do banco é morto À FORÇA com o proxy levantado. O contentor do proxy — que tem a
// chave no ambiente — desaparece sozinho dentro do prazo do vigia, e a corrida seguinte varre o
// que ficou (a rede e o contentor do provider falso).
func TestAOS512_ProxyReal_ProcessoMortoNaoDeixaOProxy(t *testing.T) {
	binario := os.Getenv("AOS_BANCO_FALSO_BIN")
	if os.Getenv("AOS_BANCO_PROXY") != "1" || binario == "" {
		t.Skip("SALTADO: AOS_BANCO_PROXY != 1 — precisa de Docker e da imagem de producao do proxy; correr com `bash scripts/ci/banco-ensaio-proxy.sh`. " +
			"POR VERIFICAR nesta execucao: que um proxy cujo processo pai morreu a forca desaparece sozinho e que a corrida seguinte varre os orfaos.")
	}
	if _, _, err := VarrerOrfaos(context.Background()); err != nil {
		t.Fatal(err)
	}
	const tolerancia = 20 * time.Second

	// (1) Morte à força ⇒ o proxy desaparece SOZINHO, dentro do prazo.
	ajudante := lancarAjudante(t, "proxy", "AOS512_TOLERANCIA="+tolerancia.String())
	if !temProxy(contentoresDoBanco(t)) {
		t.Fatal("o ajudante disse que lancou o proxy e nao ha contentor do proxy")
	}
	// A chave ESTÁ no ambiente do contentor enquanto ele existe: é por isso que tem de morrer.
	for _, n := range contentoresDoBanco(t) {
		if strings.HasPrefix(n, prefixoDoDocker+"proxy-") {
			env, _ := exec.Command("docker", "inspect", n, "--format", "{{json .Config.Env}}").Output()
			if !strings.Contains(string(env), "sk-falso-do-ajudante-0123456789") {
				t.Fatalf("premissa do teste: a chave da rota tinha de estar no ambiente do contentor")
			}
			args, _ := exec.Command("docker", "inspect", n, "--format", "{{json .Args}} {{json .Config.Cmd}}").Output()
			if strings.Contains(string(args), "sk-falso-do-ajudante-0123456789") {
				t.Errorf("a chave da rota esta nos argumentos do contentor")
			}
		}
	}
	matar(t, ajudante)
	morte := time.Now()
	limite := tolerancia + 45*time.Second
	for temProxy(contentoresDoBanco(t)) {
		if time.Since(morte) > limite {
			t.Fatalf("o proxy continua vivo %s depois de o processo pai morrer (tolerancia do vigia: %s)", time.Since(morte).Round(time.Second), tolerancia)
		}
		time.Sleep(2 * time.Second)
	}
	demorou := time.Since(morte).Round(time.Second)

	// (2) A corrida seguinte VARRE o que ficou: a rede, e o contentor do provider falso.
	contentores, redes, err := VarrerOrfaos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if redes < 1 {
		t.Errorf("a varredura tinha de remover a rede que o processo morto deixou: removeu %d rede(s) e %d contentor(es)", redes, contentores)
	}

	// (3) E varre um proxy que AINDA está vivo (o vigia ainda não actuou): morto o segundo
	// ajudante, o `limpar` remove o contentor do proxy de imediato e diz quantos removeu.
	ajudante = lancarAjudante(t, "proxy", "AOS512_TOLERANCIA=10m")
	matar(t, ajudante)
	if !temProxy(contentoresDoBanco(t)) {
		t.Fatal("o segundo proxy tinha de estar vivo (tolerancia de 10 minutos)")
	}
	e := executar(t, Ambiente{}, "limpar")
	if e.codigo != SaidaOK || !strings.Contains(e.stdout, "removidos 2 contentor(es) e 1 rede(s)") {
		t.Errorf("limpar: codigo %d: %q %q", e.codigo, e.stdout, e.stderr)
	}
	if sobra := contentoresDoBanco(t); len(sobra) != 0 {
		t.Errorf("depois do limpar ficaram contentores: %v", sobra)
	}
	resumo, _ := json.Marshal(map[string]any{"proxy_morreu_sozinho_em_s": demorou.Seconds(), "tolerancia_s": tolerancia.Seconds(),
		"varridos_contentores": contentores, "varridas_redes": redes, "pass": !t.Failed()})
	t.Logf("AOS_BANCO_ORFAO_REPORT %s", resumo)
}

// O provider falso corta o roteiro ao terceiro 401: com `disable_cooldowns`, TODOS os pedidos
// chegam ao fornecedor (nenhum 429 fabricado pelo proxy).
func TestAOS512_ProxyReal_SemArrefecimentoDoProxy(t *testing.T) {
	binario := os.Getenv("AOS_BANCO_FALSO_BIN")
	if os.Getenv("AOS_BANCO_PROXY") != "1" || binario == "" {
		t.Skip("SALTADO: AOS_BANCO_PROXY != 1 — precisa de Docker e da imagem de producao do proxy; correr com `bash scripts/ci/banco-ensaio-proxy.sh`. " +
			"POR VERIFICAR nesta execucao: que o proxy do ensaio nao responde 429 por arrefecimento depois de um 401 do fornecedor.")
	}
	saida := t.TempDir()
	e := executar(t, Ambiente{}, "proxy", "--binario-do-falso", binario, "--roteiro", string(ComportamentoErro401), "--saida", saida, "--silencioso")
	if e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	achados, _ := filepath.Glob(filepath.Join(saida, "*.json"))
	cru, _ := os.ReadFile(achados[0])
	var r Relatorio
	if err := json.Unmarshal(cru, &r); err != nil {
		t.Fatal(err)
	}
	// A bateria com tudo a 401: seis runs (o resumo do T2 não corre), um pedido cada.
	if r.Taxas.HTTP["401"] != 6 || r.Taxas.HTTP["429"] != 0 || len(r.Taxas.HTTP) != 1 {
		t.Errorf("codigos HTTP = %v, quer seis 401 e nenhum 429 (um 429 aqui e o arrefecimento do proxy)", r.Taxas.HTTP)
	}
	t.Logf("AOS_BANCO_COOLDOWN_REPORT {\"http\":%v,\"pass\":%v}", r.Taxas.HTTP["401"], !t.Failed())
}
