package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// aos451_gvisor_init_test.go — o executor gVisor de produção corre com um init no PID 1.
//
// O `/component` do `aos-gvisor` é o PID 1 do seu contentor e não recolhe processos que não
// lançou. Os `curl` do healthcheck cujo `sh` morre no timeout ficam órfãos, passam para ele e
// ficam zombies para sempre: medido em produção a 2026-09-26, 15 em ~28 dias. Com
// `pids_limit: 512` a fuga acaba por negar o `fork` ao próprio executor de sandbox.
//
// A correcção é uma linha do compose, e uma linha que alguém tira ao «limpar» o serviço sem que
// nada fique vermelho. Este teste é o sensor dessa linha.

var reInitVerdadeiro = regexp.MustCompile(`(?m)^    init:\s*true\s*$`)

func TestAOS451_GVisorCorreComInitNoPID1(t *testing.T) {
	caminho := filepath.Join("..", "..", "..", "deploy", "server", "docker-compose.prod.yml")
	b, err := os.ReadFile(caminho)
	if err != nil {
		// FALHA, não skip: sem o manifesto o sensor não mede nada.
		t.Fatalf("ler o manifesto de produção %s: %v", caminho, err)
	}
	if bloco := blocoDoServico(t, string(b), "gvisor"); !reInitVerdadeiro.MatchString(bloco) {
		t.Fatal("o serviço gvisor de deploy/server/docker-compose.prod.yml não tem `init: true` — " +
			"o `/component` fica PID 1 e os `curl` órfãos do healthcheck acumulam-se como zombies " +
			"até esgotarem o pids_limit (AOS-451)")
	}
}
