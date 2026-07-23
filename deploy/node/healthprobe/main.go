// Command aos-healthprobe é a sonda de contentor do nó `aos` numa imagem DISTROLESS.
//
// PORQUÊ um binário e não `curl`/`wget` num HEALTHCHECK: a imagem final é distroless
// STATIC (ADR-017 ponto 2) — não tem shell, nem package-manager, nem curl. O único modo
// honesto de um `HEALTHCHECK` do Docker sondar a liveness é um executável dedicado. Este
// probe é ZERO-DEP (só stdlib), compila estático (CGO off) e faz um GET à sonda de
// LIVENESS `GET /healthz` (AOS-171) no loopback do próprio contentor.
//
// Contrato: exit 0 sse a sonda devolver HTTP 200; exit != 0 caso contrário (o que faz o
// Docker/orquestrador marcar o contentor como unhealthy). NÃO transporta segredos nem
// identidade — /healthz é deliberadamente não-autenticada e sem rate-limit (AOS-171).
//
// Nota de fronteira (AOS-171): /healthz é LIVENESS (o processo está vivo), não readiness.
// Num orquestrador k8s prefira sondas httpGet nativas sobre /healthz (liveness) e /readyz
// (readiness) — ver deploy/node/README. Este probe existe para que um `docker run`
// autónomo tenha um HEALTHCHECK funcional apesar do distroless sem shell.
package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// healthURL resolve a URL de liveness a sondar, com precedência:
//
//  1. AOS_HEALTH_URL — override EXPLÍCITO do operador (se definido, ganha);
//  2. derivada de AOS_API_ADDR — a MESMA env que faz o nó levantar a API: usa-se a sua PORTA
//     no loopback do contentor (127.0.0.1:<porta>/healthz). Isto ELIMINA o acoplamento silencioso
//     a uma porta hardcoded: mudar AOS_API_ADDR para outra porta faz o probe segui-la sozinho, sem
//     obrigar o operador a manter uma segunda variável em sincronia;
//  3. default 127.0.0.1:8080/healthz — a porta convencional (EXPOSE 8080).
//
// A sonda é SEMPRE no loopback (o probe corre DENTRO do contentor), mesmo que AOS_API_ADDR faça
// bind em 0.0.0.0 — só a porta interessa.
func healthURL() string {
	if u := strings.TrimSpace(os.Getenv("AOS_HEALTH_URL")); u != "" {
		return u
	}
	port := "8080"
	if addr := strings.TrimSpace(os.Getenv("AOS_API_ADDR")); addr != "" {
		if _, p, err := net.SplitHostPort(addr); err == nil && p != "" {
			port = p
		}
	}
	return "http://127.0.0.1:" + port + "/healthz"
}

// main sonda a URL de liveness e mapeia o resultado para o código de saída do processo.
func main() {
	url := healthURL()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		os.Exit(1)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
	os.Exit(0)
}
