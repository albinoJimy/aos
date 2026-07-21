# AOS-145 — Relatório do build-spike offline sobre o tip AOS-128

| Campo | Valor |
|---|---|
| Ticket | AOS-145 (EPIC-14 — Integração / Composition-Root, PR-0.a) |
| Data | Julho de 2026 |
| Base | `feature/AOS-128-ux-dx-tests` (41 módulos) |
| Método | Worktree isolado (`git worktree`), build/test **offline** `GOPROXY=off GOFLAGS=-mod=mod`; medir, não corrigir |
| Seams medidos | RM `NewProduction` (`claude/eloquent-colden`), Model Gateway `NewProduction`+routing/failover (`claude/nice-cartwright`), os 2 `packages/integration` (`nice` + `admiring`) |

---

## 1. Objectivo

Provar que o tip real AOS-128 compila/testa offline e **medir** a superfície de drift/porting ao sobrepor os seams resgatados (AOS-144), mais a colisão dos dois `packages/integration`. Sem este spike, nenhuma estimativa de PR-0.a era defensável.

## 2. Resultados (todos verificados por build/test real)

| Medição | Resultado |
|---|---|
| **Tip AOS-128 (41 módulos), build+test offline** | **41/41 PASS**, 0 FAIL |
| **Colisão dos 2 `integration` — ficheiros** | Disjuntos: `nice` = 2 `.go` (Model Gateway); `admiring` = 8 `.go` (freeze/reval). **0 colisões de nome** (.go); única sobreposição = `go.mod` |
| **Colisão dos 2 `integration` — símbolos** | `nice` 3 símbolos top-level, `admiring` 25 — **0 em comum**. Nenhuma colisão de símbolo package-level (o `ErrNoModel` receado não colide) |
| **`go.mod` unido** | União dos `require`/`replace`; sem conflito de versão (tudo `v0.0.0` local) |
| **Seam RM** (`production.go` novo + `taint_gate.go`, 18 linhas de drift) | **build + test GREEN** contra o tip — porting = **0** |
| **Seam Model Gateway** (`production.go`+`routing/failover` novos + `gateway.go`, **149 linhas** de drift) | Ficheiros novos **build + test GREEN** contra o `gateway.go` **do tip** — porting = **0** |
| **`integration` UNIFICADO** (10 `.go` disjuntos + fecho de 41 `replace` + `go.sum` unido) | **build + test + vet GREEN** (incl. os dois e2e resgatados: `modelgateway_e2e` + `revalidation_e2e` + `wiring_unit`) |

## 3. Conclusões

1. **A consolidação (PR-0.a) é essencialmente MECÂNICA — não há porting.** O receio central do painel (drift de ~63–81 commits + colisão dos dois ápices) **não se materializou** em falhas de build/test: os dois seams caem no tip verdes, e os dois `integration` unem-se sem colisão de ficheiro **nem** de símbolo. O `gateway.go` divergia 149 linhas, mas o tip já acomoda os ficheiros novos do seam.

2. **O único trabalho real de `go.mod` é o fecho de `replace` re-declarado.** Como os `replace` não são transitivos, o módulo `integration` precisa de **todos** os módulos locais re-declarados (41 no spike). É mecânico e gerável automaticamente:
   ```bash
   find packages -name go.mod ! -path '*/integration/*' \
     | sed 's|packages/||;s|/go.mod||' \
     | while read rp; do echo "replace github.com/aos-ref/$rp => ../$rp"; done
   ```

3. **Caveat que sobrevive: determinismo offline é local-à-máquina.** Os builds `GOPROXY=off` só passam com o `GOMODCACHE` quente (sem `vendor/`). Um runner de CI frio falha. **Continua a exigir AOS-148** (vendoring ou cache-prime pinado) — é o verdadeiro trabalho de PR-0.a, não o porting.

## 4. Revisão de estimativas de PR-0.a

| Ticket | Estimativa v1.0 | Revisão pós-spike | Fundamento |
|---|---|---|---|
| AOS-146 (reconciliar os 2 `integration`) | M | **S** | Ficheiros e símbolos disjuntos; `go.mod` = união gerável; build+test já provados verdes |
| AOS-147 (fold dos seams + porting) | M | **S** | Porting medido = 0 (ambos os seams verdes contra o tip) |
| AOS-148 (gates + offline reproduzível) | M | **M (inalterado)** | O trabalho real de PR-0.a: vendoring/cache-prime + gate `apex.sh` |

**PR-0.a passa de ~M+M+M para ~S+S+M.** O grosso do risco de consolidação está eliminado; o esforço restante concentra-se na reprodutibilidade offline (AOS-148), não na integração de código.

## 5. Recomendação

Avançar para **AOS-146** (reconciliar os dois `integration` num módulo único **no trunk**) — agora com risco conhecido-baixo, seguindo o método provado neste spike: união dos 10 ficheiros disjuntos + `go.mod` com o fecho de 41 `replace` + `go.sum` unido. Depois AOS-147 (fold dos seams, porting≈0) e AOS-148 (o trabalho de facto: offline reproduzível + gate).

> Nota de método: o spike correu num worktree descartável (`git worktree`), sem tocar no trunk; nenhum código foi commitado a partir dele. Este relatório é o único artefacto. As medições são reproduzíveis com os mesmos comandos offline sobre o tip.
