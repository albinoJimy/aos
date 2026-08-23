package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// --- duplos ---------------------------------------------------------------------------

type drenoHTTPFalso struct {
	bloqueiaAteExpirar bool
	fechado            bool
	prazoRecebido      time.Duration
}

func (d *drenoHTTPFalso) Shutdown(ctx context.Context) error {
	if dl, ok := ctx.Deadline(); ok {
		d.prazoRecebido = time.Until(dl)
	}
	if !d.bloqueiaAteExpirar {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}
func (d *drenoHTTPFalso) Close() error { d.fechado = true; return nil }

type drenoServicoFalso struct {
	chamado     bool
	erroDoCtx   error
	prazoVivo   time.Duration
	temDeadline bool
}

func (d *drenoServicoFalso) Shutdown(ctx context.Context) error {
	d.chamado = true
	d.erroDoCtx = ctx.Err()
	if dl, ok := ctx.Deadline(); ok {
		d.temDeadline = true
		d.prazoVivo = time.Until(dl)
	}
	return nil
}

// --- a cablagem, que e o achado -------------------------------------------------------

// O DRENO DO SERVICO RECEBE UM CONTEXTO VIVO MESMO QUANDO O DRENO HTTP ESGOTOU O DELE.
//
// E este o defeito: com um contexto PARTILHADO, um unico SSE ocioso segurava o
// `srv.Shutdown` ate ao fim do prazo e o `svc.Shutdown` recebia-o ja expirado — cancelava
// os runs em vez de os drenar, sob uma linha de log que prometia o contrario.
func TestEncerrar_ServicoRecebeContextoVivoApesarDoHTTPEsgotar(t *testing.T) {
	orc := orcamentoDeEncerramento{HTTP: 60 * time.Millisecond, Servico: 300 * time.Millisecond}
	srv := &drenoHTTPFalso{bloqueiaAteExpirar: true}
	svc := &drenoServicoFalso{}
	var log strings.Builder

	encerrarGraciosamente(&log, srv, svc, orc)

	if !svc.chamado {
		t.Fatal("o dreno do servico nem sequer foi chamado")
	}
	if svc.erroDoCtx != nil {
		t.Fatalf("o servico recebeu um contexto JA EXPIRADO (%v) — e o defeito original: o prazo foi gasto pelo dreno HTTP", svc.erroDoCtx)
	}
	if !svc.temDeadline {
		t.Fatal("o servico recebeu um contexto SEM prazo — um dreno sem tecto pendura o encerramento")
	}
	// O prazo tem de ser o DELE, nao as sobras. Com 300ms de orcamento, restar menos de
	// metade significaria que veio de um relogio partilhado com o dreno HTTP.
	if svc.prazoVivo < orc.Servico/2 {
		t.Fatalf("o servico recebeu %v de prazo vivo, esperava perto de %v — parece sobra e nao orcamento proprio", svc.prazoVivo, orc.Servico)
	}
	if !srv.fechado {
		t.Fatal("o dreno HTTP excedeu o orcamento e o Close NAO foi chamado — as ligacoes remanescentes ficariam a competir com o dreno do servico")
	}
	if !strings.Contains(log.String(), "dreno HTTP excedeu") {
		t.Fatalf("o operador nao foi avisado do fecho forcado; log=%q", log.String())
	}
}

// CONTROLO ANTI-VACUIDADE. Sem este caso, uma implementacao que chamasse SEMPRE o `Close` e
// desse SEMPRE um contexto novo passaria o teste de cima sem distinguir coisa nenhuma. Aqui
// o dreno HTTP acaba dentro do orcamento: o `Close` NAO pode ser chamado (fecharia a martelo
// ligacoes que ja tinham terminado sozinhas) e o servico continua a receber o seu prazo.
func TestEncerrar_HTTPDentroDoOrcamentoNaoForcaFecho(t *testing.T) {
	orc := orcamentoDeEncerramento{HTTP: 200 * time.Millisecond, Servico: 300 * time.Millisecond}
	srv := &drenoHTTPFalso{bloqueiaAteExpirar: false}
	svc := &drenoServicoFalso{}
	var log strings.Builder

	encerrarGraciosamente(&log, srv, svc, orc)

	if srv.fechado {
		t.Fatal("CONTROLO: o dreno HTTP acabou a tempo e o Close foi chamado na mesma — o teste de cima nao estaria a distinguir nada")
	}
	if svc.erroDoCtx != nil {
		t.Fatalf("CONTROLO: o servico recebeu um contexto expirado (%v)", svc.erroDoCtx)
	}
	if strings.Contains(log.String(), "dreno HTTP excedeu") {
		t.Fatalf("CONTROLO: avisou de um fecho forcado que nao aconteceu; log=%q", log.String())
	}
}

// CADA DRENO RECEBE O SEU, e nao o total. Um `srv.Shutdown` que recebesse o orcamento
// INTEIRO voltaria a poder consumi-lo todo — o defeito por outra porta.
func TestEncerrar_HTTPRecebeSoAFatiaDele(t *testing.T) {
	orc := orcamentoDeEncerramento{HTTP: 80 * time.Millisecond, Servico: 400 * time.Millisecond}
	srv := &drenoHTTPFalso{bloqueiaAteExpirar: false}
	encerrarGraciosamente(io.Discard, srv, &drenoServicoFalso{}, orc)

	if srv.prazoRecebido > orc.HTTP {
		t.Fatalf("o dreno HTTP recebeu %v de prazo, mais do que a fatia dele (%v)", srv.prazoRecebido, orc.HTTP)
	}
	if srv.prazoRecebido <= 0 {
		t.Fatalf("o dreno HTTP recebeu %v — sem prazo util", srv.prazoRecebido)
	}
}

// --- a reparticao (funcao pura) -------------------------------------------------------

func TestRepartir(t *testing.T) {
	casos := []struct {
		nome  string
		total time.Duration
	}{
		{"o default", DefaultShutdownBudget},
		{"total zero cai no default", 0},
		{"total negativo cai no default", -5 * time.Second},
		{"total grande", 30 * time.Second},
		{"total minusculo (o piso morde)", time.Millisecond},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			o := repartirEncerramento(tc.total)
			if o.HTTP < pisoDeDreno || o.Servico < pisoDeDreno {
				t.Fatalf("%+v: um dos drenos ficou abaixo do piso %v — orcamento zero e cancelamento imediato, nao dreno curto", o, pisoDeDreno)
			}
			// A MAIORIA E DO SERVICO: e ele que faz o trabalho duravel (leases, selos).
			// Sem esta asercao, inverter as fatias passaria despercebido.
			if tc.total >= time.Second && o.Servico <= o.HTTP {
				t.Fatalf("%+v: a fatia do servico (%v) nao e maior que a do HTTP (%v)", o, o.Servico, o.HTTP)
			}
		})
	}
}

// O TOTAL TEM DE CABER NA FOLGA DO DOCKER. Sem `stop_grace_period` declarado vale o default
// de 10s entre SIGTERM e SIGKILL; um orcamento maior seria morto a meio do dreno do servico,
// que e pior do que o cancelamento ordenado que ja existia.
func TestRepartir_NaoExcedeOTotal(t *testing.T) {
	for _, total := range []time.Duration{time.Second, DefaultShutdownBudget, 30 * time.Second} {
		o := repartirEncerramento(total)
		if o.HTTP+o.Servico > total {
			t.Fatalf("total=%v repartido em %v+%v=%v — excede a folga e traz o SIGKILL a meio",
				total, o.HTTP, o.Servico, o.HTTP+o.Servico)
		}
	}
}

func TestEncerramentoDoAmbiente(t *testing.T) {
	t.Setenv("AOS_SHUTDOWN_BUDGET", "")
	if o, err := encerramentoDoAmbiente(); err != nil || o != repartirEncerramento(DefaultShutdownBudget) {
		t.Fatalf("vazio devia dar o default; o=%+v err=%v", o, err)
	}
	t.Setenv("AOS_SHUTDOWN_BUDGET", "30s")
	if o, err := encerramentoDoAmbiente(); err != nil || o != repartirEncerramento(30*time.Second) {
		t.Fatalf("30s: o=%+v err=%v", o, err)
	}
	// FAIL-CLOSED NA LEITURA: um valor ilegivel nao vira default em silencio.
	for _, mau := range []string{"nao-e-duracao", "0", "-1s"} {
		t.Setenv("AOS_SHUTDOWN_BUDGET", mau)
		if _, err := encerramentoDoAmbiente(); !errors.Is(err, ErrBadShutdownBudget) {
			t.Fatalf("AOS_SHUTDOWN_BUDGET=%q devia dar ErrBadShutdownBudget, veio %v", mau, err)
		}
	}
}

// O DEFAULT TEM DE CABER, COM MARGEM, NA FOLGA POR OMISSAO DO DOCKER.
//
// Um orcamento IGUAL a folga gasta-a inteira e o processo e morto na fronteira — e o
// ultimo dreno a correr, o do servico, e o que perde. Esta asercao amarra a constante a
// razao de ser dela: sem ela, subir o default para 10s ou 15s passaria despercebido e
// reintroduzia o SIGKILL a meio do dreno por outra porta.
func TestDefault_CabeNaFolgaDoDockerComMargem(t *testing.T) {
	if DefaultShutdownBudget >= dockerDefaultGrace {
		t.Fatalf("DefaultShutdownBudget=%v >= folga do Docker=%v — o SIGKILL chega a meio do dreno do servico",
			DefaultShutdownBudget, dockerDefaultGrace)
	}
	if margem := dockerDefaultGrace - DefaultShutdownBudget; margem < time.Second {
		t.Fatalf("so %v de margem entre o orcamento e o SIGKILL — nao chega para tratar o sinal e esvaziar o log", margem)
	}
}
