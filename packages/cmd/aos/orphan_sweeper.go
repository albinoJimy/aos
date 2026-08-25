package main

// RE-VARREDURA PERIÓDICA DE ÓRFÃOS (achado A4 da auditoria de 2026-08-17).
//
// O QUE FECHA. A varredura de crash-resume corria SÓ NO ARRANQUE. Num nó ÚNICO isso deixa um
// buraco observado em produção: mata-se o nó a meio de um run e, ao arrancar, a varredura ENCONTRA
// o órfão e SALTA-O — o lease da encarnação ANTERIOR DO MESMO PROCESSO ainda não expirou (TTL de
// 2 min) e a varredura recusa-se, correctamente, a roubar partição. Depois disso nada re-varre.
// O run fica em `running` para sempre, com trabalho real feito e durável, e a API responde `404`
// — indistinguível de "nunca existiu" — até alguém reiniciar OUTRA VEZ.
//
// Em multi-réplica o buraco não existe: outra réplica reclama o lease e retoma. É comportamento
// correcto para multi-réplica cujo custo é pago inteiro por quem corre uma réplica só — que é a
// topologia deste nó, declarada no README.
//
// A CORRECÇÃO QUE NÃO SE FEZ, e porquê. A tentação é reconhecer que o lease é da encarnação
// anterior do MESMO processo e reclamá-lo de imediato. Seria errado: se por qualquer razão o
// processo antigo ainda estiver vivo (um restart que não matou o anterior, um relógio a andar
// para trás), reclamar produz DUPLA EXECUÇÃO — exactamente o que o lease existe para impedir.
// A re-varredura periódica não enfraquece invariante nenhum: respeita o lease e limita-se a
// tentar outra vez depois de ele expirar. O próprio varredor declara-se "idempotente e seguro de
// re-correr", e é esse contrato que esta peça usa.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ErrBadOrphanSweepInterval — AOS_CRASH_RESUME_INTERVAL está definida mas não é uma duração Go.
// Fail-closed: quem a declara obtém-na bem-formada ou o nó recusa arrancar. Um valor ilegível
// aceite em silêncio deixaria a re-varredura no default enquanto quem o escreveu julgaria tê-la
// afinado — e o sintoma só apareceria num crash, que é quando menos se quer surpresas.
var ErrBadOrphanSweepInterval = errors.New("aos: AOS_CRASH_RESUME_INTERVAL mal configurada — duracao Go (ex.: 2m, 30s); 0 DESLIGA a re-varredura periodica e deixa so a de arranque")

// parseOrphanSweepInterval lê AOS_CRASH_RESUME_INTERVAL. Vazia ⇒ [DefaultOrphanSweepInterval].
func parseOrphanSweepInterval() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_CRASH_RESUME_INTERVAL"))
	if raw == "" {
		return DefaultOrphanSweepInterval, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrBadOrphanSweepInterval, raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("%w: %q e negativa", ErrBadOrphanSweepInterval, raw)
	}
	return d, nil
}

// DefaultOrphanSweepInterval é o intervalo por omissão entre re-varreduras. Vale o TTL do lease:
// mais curto seria varrer sobre leases que ainda não podiam ser reclamados (trabalho sem efeito
// possível), e mais longo alargaria a janela em que um órfão fica inalcançável sem necessidade.
// Com este valor, um run órfão é apanhado dentro de ~2 TTL do crash — o primeiro ciclo ainda
// encontra o lease vivo, o seguinte já o encontra expirado.
const DefaultOrphanSweepInterval = DefaultLeaseTTL

// StartOrphanSweeper re-corre a varredura de órfãos periodicamente até `ctx` terminar.
//
// Corre em goroutine própria e devolve imediatamente. Silencioso quando não há nada — só declara
// quando encontra órfãos (ver `anuncia` em [NodeService.resumeInterruptedRuns]), para que uma
// linha de crash-resume no log signifique sempre alguma coisa.
//
// `interval <= 0` DESLIGA a re-varredura e devolve sem arrancar goroutine nenhuma: fica só a
// varredura de arranque, o comportamento anterior. É opt-out explícito, não silencioso.
//
// FALHAS: uma falha de COMPOSIÇÃO do varredor aborta o ARRANQUE (fail-closed, em [main]); aqui,
// já com o nó a servir, uma falha de um ciclo é declarada e o ciclo seguinte tenta de novo —
// derrubar um nó saudável porque uma re-varredura falhou trocaria um órfão por uma paragem.
func (s *NodeService) StartOrphanSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		s.log("re-varredura de orfaos (AOS-253/A4): DESLIGADA por configuracao — fica so a varredura de ARRANQUE. Num no UNICO isso significa que um run interrompido por crash so e retomado no SEGUNDO arranque, porque no primeiro o lease da encarnacao anterior ainda nao expirou")
		return
	}
	s.log("re-varredura de orfaos (AOS-253/A4): LIGADA a cada %s — um run interrompido por crash e retomado sem exigir um segundo arranque manual. Respeita o lease: nunca rouba particao, apenas volta a tentar depois de ele expirar", interval)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, _, err := s.resumeInterruptedRuns(ctx, false); err != nil {
					s.log("re-varredura de orfaos FALHOU neste ciclo (o proximo tenta de novo): %v", err)
				}
				// Conta-se a PASSAGEM, mesmo quando ela falhou: o que a idade denuncia e o
				// laco ter PARADO, e um ciclo que correu e falhou nao e um laco parado — essa
				// distincao vive no log, nao aqui.
				s.passagensOrfaos.Add(1)
				s.ultimoOrfaoUnix.Store(time.Now().Unix())
			}
		}
	}()
}
