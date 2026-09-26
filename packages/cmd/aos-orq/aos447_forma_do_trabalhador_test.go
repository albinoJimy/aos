package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// AOS-447 — a forma do trabalhador, decidida pelo dono: UM trabalhador, o timer
// `aos-drenar-planos` 1 min depois da drenagem anterior, e UM pedido por drenagem. Estes testes
// fixam a decisão nos ficheiros que a aplicam — um lado a voltar atrás sozinho (o timer a 5 min, o
// máximo a 3) repunha a latência medida em 2026-09-25 sem que nada o dissesse.

func TestAOS447TimerDeUmMinutoEUmPedidoPorDrenagem(t *testing.T) {
	timer := lerDoRepo(t, "deploy", "server", "systemd", "aos-drenar-planos.timer")
	for _, quer := range []string{"\nOnUnitInactiveSec=1min\n", "\nAccuracySec=5s\n"} {
		if !strings.Contains(timer, quer) {
			t.Errorf("aos-drenar-planos.timer já não tem %q", strings.TrimSpace(quer))
		}
	}
	// O UM vive na UNIDADE, ao lado do timer (revisão do AOS-445/447): o script chega pelo rsync em
	// cada deploy e as unidades só quando o root as reinstala — com o 1 como omissão do script, entre
	// um e outro a fila drenava 1 pedido de 5 em 5 min. À mão, sem a variável, continua 3.
	servico := lerDoRepo(t, "deploy", "server", "systemd", "aos-drenar-planos.service")
	if !strings.Contains(servico, "\nEnvironment=DRENAR_MAX=1\n") {
		t.Error("aos-drenar-planos.service já não fixa Environment=DRENAR_MAX=1")
	}
	drenar := lerDoRepo(t, "deploy", "server", "drenar-planos.sh")
	if !strings.Contains(drenar, `MAX="${DRENAR_MAX:-3}"`) {
		t.Error(`drenar-planos.sh mudou a omissão do máximo (MAX="${DRENAR_MAX:-3}") — o 1 é da unidade`)
	}
	// Os comentários que diziam «a cada 5 min» enganavam quem lê o deploy e o CD — é por eles que se
	// raciocina sobre a janela do AOS-450.
	for _, f := range [][]string{
		{"deploy", "server", "drenar-planos.sh"},
		{"deploy", "server", "deploy.sh"},
		{".github", "workflows", "deploy.yml"},
	} {
		if s := lerDoRepo(t, f...); strings.Contains(s, "a cada 5 min") {
			t.Errorf("%s ainda diz que a drenagem corre «a cada 5 min»", filepath.Join(f...))
		}
	}
}

// TestAOS447DecideBloqueadoEnquantoUmServeDetemOWAL mede o primeiro achado do desenho: o `decide` da
// cerimónia de aprovação toma posse de ESCRITA do mesmo WAL do `consume` (`consume.wal`), e o
// `serve` que o `consume` corre por cada pedido detém essa posse durante o plano inteiro (o
// `abrirParaEscrita` do cmdServe só a larga no fim). Enquanto um plano corre, um `decide` sai com 5
// (WAL detido — transitório: repete-se). A posse é tomada aqui pelo teste, no papel desse `serve`.
func TestAOS447DecideBloqueadoEnquantoUmServeDetemOWAL(t *testing.T) {
	dir := t.TempDir()
	wal := filepath.Join(dir, "consume.wal")
	largar, err := eventstore.LockWAL(wal)
	if err != nil {
		t.Fatalf("posse do WAL (o serve do consume): %v", err)
	}
	defer func() { _ = largar() }()

	nada := filepath.Join(dir, "nao-lido.json") // o decide recusa ANTES de ler qualquer destes
	err = cmdDecide([]string{"--wal", wal, "--run", "plan-447", "--plan-doc", nada, "--snapshot", nada,
		"--decision", "approve", "--approval", nada, "--approvers", nada})
	if !errors.Is(err, eventstore.ErrWALHeld) {
		t.Fatalf("o decide com o WAL detido tinha de falhar com ErrWALHeld; veio %v", err)
	}
	if got := codigoDe(err); got != exitWALDetido {
		t.Fatalf("código de saída %d, quer %d (exitWALDetido)", got, exitWALDetido)
	}

	// Largado o WAL (o plano acabou), o mesmo decide passa da posse e vai ler o documento — que aqui
	// não existe. Prova que o 5 era do WAL e não de outra coisa.
	if err := largar(); err != nil {
		t.Fatal(err)
	}
	if err := cmdDecide([]string{"--wal", wal, "--run", "plan-447", "--plan-doc", nada, "--snapshot", nada,
		"--decision", "approve", "--approval", nada, "--approvers", nada}); errors.Is(err, eventstore.ErrWALHeld) || err == nil {
		t.Fatalf("com o WAL livre o decide tinha de passar da posse (e falhar mais à frente); veio %v", err)
	}
}
