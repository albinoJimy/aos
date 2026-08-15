# Incidente 2026-08-14 — certificado expirado em `api.elysiumii.site`

**Servidor:** `37.60.241.150` (control-plane Kubernetes v1.29.15, kubeadm).
**Estado:** resolvido para o certificado. Causas de fundo **abertas** (§5).

Este runbook não pertence ao AOS: pertence ao servidor onde o AOS vai correr. Está aqui porque
foi durante o levantamento para o CD que a avaria apareceu, e porque a decisão de topologia do
AOS (Compose no host, não Kubernetes) só se entende à luz do que está escrito abaixo.

---

## 1. Sintoma

`https://api.elysiumii.site` apresentava um certificado Let's Encrypt **expirado a 08/08/2026**.
Seis dias a ser rejeitado por qualquer cliente que valide a cadeia.

## 2. Cadeia causal

| Data | Acontecimento |
|---|---|
| 09 Jul | `vmi3075398` deixa de reportar. No mesmo dia o cert-manager agenda a renovação e cria o Order |
| 14 Jul | `vmi2911680` e `vmi2911681` param — os pods do cert-manager caem com elas |
| 20 Jul | `vmi3313361` para |
| 03 Ago | `vmi3002938` para — último worker |
| 08 Ago | Certificado de `api.elysiumii.site` expira |
| 14 Ago | Reparação |

Cinco VMs worker desligadas/eliminadas (SSH **e** kubelet fechados em todas). Restou o
control-plane, com o taint `node-role.kubernetes.io/control-plane:NoSchedule`. Sem nó onde
agendar, caíram em cascata: **cert-manager** (3 Deployments), **istiod**, **metrics-server** e
outros 45 pods.

**Dois bloqueios em série, e o segundo é o que interessa reter:**

1. Os pods do cert-manager não agendavam → ninguém executava o Challenge ACME.
2. Mesmo com o cert-manager de pé, o **injector de sidecars do Istio** impedia a criação do pod
   solver: os quatro webhooks de `istio-sidecar-injector` têm `failurePolicy=Fail` e o `istiod`
   estava `Pending`, pelo que **nenhum pod podia nascer** em `auth`, `neural-hive`,
   `neural-hive-mcp`, `observability` e `redis-cluster`. 39 `FailedCreate` registados.

O Order original (36 dias) já era irrecuperável por outra razão: as autorizações ACME caducam
em ~7 dias e a Let's Encrypt devolvia `404 ... No order for ID 530349411476`.

## 3. O que foi alterado

Backup do estado anterior em **`/root/aos-cert-fix-backup/`** no servidor.

**(a) Tolerância ao taint nos 3 Deployments do cert-manager** — antes tinham a lista vazia:

```bash
kubectl patch deploy -n cert-manager <cert-manager|cert-manager-cainjector|cert-manager-webhook> \
  --type=strategic -p '{"spec":{"template":{"spec":{"tolerations":[
    {"key":"node-role.kubernetes.io/control-plane","operator":"Exists","effect":"NoSchedule"}]}}}}'
```

⚠️ **A ordem é obrigatória.** `webhook.cert-manager.io` tem `failurePolicy=Fail`: enquanto o
webhook está em baixo, **qualquer alteração a um `ClusterIssuer` ou `Certificate` é rejeitada**.
Primeiro os Deployments (que este webhook não valida), esperar Ready, só depois o resto.

**(b) `ClusterIssuer/letsencrypt-prod` — solver com tolerância e fora do injector:**

```bash
kubectl patch clusterissuer letsencrypt-prod --type=merge -p '{
  "spec":{"acme":{"solvers":[{"http01":{"ingress":{"class":"nginx","podTemplate":{
    "metadata":{"labels":{"sidecar.istio.io/inject":"false"}},
    "spec":{"tolerations":[{"key":"node-role.kubernetes.io/control-plane","operator":"Exists","effect":"NoSchedule"}]}
  }}}}]}}}'
```

A etiqueta `sidecar.istio.io/inject: "false"` é a peça não-óbvia. Os webhooks do Istio têm
`objectSelector: sidecar.istio.io/inject NotIn ["false"]` — um pod com essa etiqueta **não chega
a invocar o webhook**, pelo que o API server o cria mesmo com o `istiod` morto. Foi o que
permitiu resolver o certificado **sem tocar no Istio**.

**(c) Order e CertificateRequest caducados apagados**, e o *backoff* de emissão limpo (o
equivalente a `kubectl cert-manager renew`, que não está instalado):

```bash
kubectl delete order -n neural-hive neural-hive-gateway-tls-4-983788063
kubectl delete certificaterequest -n neural-hive neural-hive-gateway-tls-4
kubectl patch certificate -n neural-hive neural-hive-gateway-tls --subresource=status \
  --type=merge -p '{"status":{"lastFailureTime":null,"failedIssuanceAttempts":null}}'
kubectl delete challenge -n neural-hive --all
```

**(d) `istiod` e `metrics-server` levantados** com a mesma tolerância (backups em `$BK/istiod.yaml`
e `$BK/metrics-server.yaml`; ambos tinham a lista de tolerations vazia):

```bash
kubectl patch deploy -n istio-system istiod    --type=strategic -p "$TOL"
kubectl patch deploy -n kube-system metrics-server --type=strategic -p "$TOL"
```

`istiod` pronto em 30 s, `metrics-server` em 60 s. Com o `istiod` de pé, o injector de sidecars
voltou a responder e a criação de pods **desbloqueou nos 5 namespaces** (verificado com
`kubectl run --dry-run=server` em `neural-hive`, que passou; zero `FailedCreate` novos).

> A ordem entre (b) e (d) importa menos do que parece, mas a etiqueta `sidecar.istio.io/inject:
> "false"` no solver **deve ficar**: com o `istiod` vivo, um solver ACME injectado com sidecar
> pode falhar o desafio HTTP-01 por interceptação de tráfego.

O `istio-ingressgateway` **não** foi levantado — não é preciso para o webhook e o seu Service é um
LoadBalancer com `EXTERNAL-IP` pendente (não há provedor de LB neste cluster).

## 4. Resultado

`notBefore=Aug 14 18:56:06 2026` · `notAfter=Nov 12 18:56:05 2026`, cadeia validada
(`curl` sem `-k`, `ssl_verify=0`).

A correcção é **sistémica, não pontual**: as duas alterações vivem no `ClusterIssuer`, pelo que
`longhorn-tls` (expira 06/09) e `grafana-tls` (25/09) renovam sozinhos quando chegar a altura.

> `api.elysiumii.site` responde **503** — o TLS está resolvido, mas o serviço por trás do ingress
> não corre. É consequência das VMs mortas, não da renovação.

**(e) Stack órfão do Coolify parado** (28 containers, `docker stop -t 30`, aplicações antes das
bases de dados). **Nenhum `rm`; os 28 volumes ficaram intactos.**

Descoberta pelo caminho: **não existe um único ficheiro `docker-compose.yml` em `/data/coolify`.**
O `docker compose ls` listava caminhos que já foram apagados — o Coolify foi removido e levou as
definições, deixando os containers vivos e não-geríveis por compose. Por isso a reposição é por
**nome de container**, não por projecto:

```bash
bash /root/aos-cert-fix-backup/coolify-restore.sh          # tudo
```

Listas em `$BK/coolify-apps.txt` e `$BK/coolify-datastores.txt`.

Efeito medido: **CPU 70–81% → 35–41%**, memória 34% → 20%, *load average* ~31 → **5,0**.

## Reverter

```bash
kubectl apply -f /root/aos-cert-fix-backup/cert-manager.yaml            # e cainjector / webhook
kubectl apply -f /root/aos-cert-fix-backup/clusterissuer-letsencrypt-prod.yaml
```

Reverter devolve o cluster ao estado partido — só faz sentido se alguma destas alterações vier a
revelar-se causa de outro problema.

---

## 5. O que continua aberto

1. ~~**Certificados do kubeadm expiram a 29/10/2026 (75 dias).**~~ **FECHADO em 2026-08-15** —
   renovados para **15/08/2027 (364 dias)**. Procedimento em §6.
2. **O verdadeiro consumidor da máquina não é o Kubernetes.** Com o `metrics-server` de pé foi
   possível medir: os pods somam ~2 cores, mas o nó usa 5,6–6,5 de 8. A diferença são os
   **containers Docker órfãos do Coolify — ~4,8 cores (60% da máquina)** — para serviços que
   *nada encaminha*, porque o control-plane e o proxy do Coolify já não existem. O pior é o
   **`rabbitmq` sozinho a 153%** (1,5 cores), preso num ciclo de diagnóstico.

   É a alavanca de capacidade mais rentável do servidor: parar o stack órfão liberta **mais** do
   que os 5,5 cores que os pods Pending pedem. Os volumes Docker não são afectados por um `stop`
   — os dados de Nextcloud/n8n/Chatwoot ficam intactos. Exige decisão do dono sobre o que ainda
   interessa.
3. **5 nós inalcançáveis, 157 pods fantasma.** O cluster reporta ~230 pods e só **16** correm.
   Remover os nós despeja os fantasmas e liberta o node controller.
4. **Duas StorageClasses marcadas `(default)`** (`local-path` e `longhorn`): PVCs sem classe
   explícita ficam com comportamento indefinido.
5. **O bloqueio agora é o TAINT, não a capacidade.** Depois de parar o stack órfão, o nó tem
   ~4,9 cores e ~18 Gi de *requests* livres. Mas os **89** pods Pending pedem **15,5 cores e
   32 Gi** — mais do que a máquina inteira (8 / 23). Foram desenhados para um cluster de 6 nós.

   O escalonador rejeita-os por `untolerated taint {node-role.kubernetes.io/control-plane}`, não
   por falta de espaço. Remover o taint **não resolveria**: encheria o nó com os pods que o
   escalonador calhasse apanhar primeiro e deixaria o resto Pending — uma lotaria, não um plano.
   A operação correcta é **curadoria**: decidir o que tem de viver e dar tolerância só a esses,
   escalando o resto a zero.
6. **`api.elysiumii.site` responde 503** — os pods do gateway não agendam, pelo ponto 5.
7. **O namespace `auth` não consegue criar pods — e agora por outra razão.** O `LimitRange`
   `auth-limits` exige `min memory 256Mi` e `max cpu 500m` **por container**; os defaults do
   sidecar do Istio são `requests.memory 128Mi` e `limits.cpu 2000m`. Com o `istiod` vivo, o
   sidecar é injectado e **viola as duas regras** — admissão recusada.

   Não é regressão: antes o namespace também não criava pods (falhava no webhook em vez do
   LimitRange), e `auth` está vazio, pelo que nada está em baixo. Provado por A/B: o mesmo pod
   com `sidecar.istio.io/inject: "false"` **passa**.

   Três saídas, todas decisão de política: alargar o `LimitRange`; sobrepor os recursos do
   sidecar (`sidecar.istio.io/proxyCPULimit`, `proxyMemory`); ou tirar `istio-injection=enabled`
   ao namespace.

---

## 6. Renovação dos certificados do kubeadm (feita em 2026-08-15)

Expiravam a 29/10/2026; ficaram em **15/08/2027 (364 dias)**. As CAs continuam até 2035 — só
os certificados-folha é que rodam anualmente.

**Backup primeiro, sempre:** `/root/k8s-pki-backup-20260815-022335` (PKI completo + os quatro
kubeconfigs + o `/root/.kube/config`). Reverter é repor essa árvore e reiniciar os static pods.

```bash
kubeadm certs renew all          # só ESCREVE os ficheiros; os processos em curso não notam
kubeadm certs check-expiration   # confirmar as novas datas ANTES de reiniciar
```

O reinício é a parte que interrompe. Em vez de mover os manifestos de
`/etc/kubernetes/manifests` — que deixa o cluster sem control-plane se algo correr mal a meio —
removem-se os **containers**, com os manifestos intactos: o kubelet recria-os em segundos.

```bash
export CONTAINER_RUNTIME_ENDPOINT=unix:///run/containerd/containerd.sock
for c in etcd kube-apiserver kube-controller-manager kube-scheduler; do
  crictl rm -f "$(crictl ps -q --name "^${c}$")"
done
```

> Na prática bastou remover o `etcd`: os outros três seguiram por cascata (as *liveness probes*
> falharam sem ele) e o kubelet recriou os quatro. O apiserver voltou a responder em **15 s**.

**Verificar pelo que é servido, não pelo que está em disco** — é a única prova de que os
processos pegaram nos certificados novos:

```bash
echo | openssl s_client -connect 127.0.0.1:6443 2>/dev/null | openssl x509 -noout -dates
```

**Depois:** `/root/.kube/config` é uma cópia do `admin.conf` e **não** é actualizado pela
renovação. Repor com `cp -a /etc/kubernetes/admin.conf /root/.kube/config`. O kubelet tem
`rotateCertificates: true` e roda o seu sozinho — não precisa de intervenção.

**O que não foi afectado:** o nó AOS atravessou tudo sem falhar um pedido (`Up 34 minutes
(healthy)`, `HTTP 200` de fora durante e depois). Corre em Docker, fora do cluster — a
independência é por desenho, e esta foi a primeira vez que ficou demonstrada.
