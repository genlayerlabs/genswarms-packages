# GenSwarms Packages (`gsp`) — Documento de diseño

**Estado:** borrador de diseño · v0.2
**Ámbito:** índice de paquetes + CLI para representaciones intermedias (IR) de swarms de GenSwarms, y el modelo de mutación en vivo sobre el runtime.

> **Cambio v0.1 → v0.2:** los swarms mutan en caliente mientras están activos. Eso resuelve la pregunta abierta #1 (overlay-only vs in-place → **overlay es el control plane**) y obliga a modelar el ciclo de vida (tres capas, desired/observed) y la semántica de transición por-op. Secciones nuevas: §3 y §4.

---

## 1. Qué es `gsp` (y qué no es)

`gsp` es el sistema de empaquetado, distribución y composición de swarms de GenSwarms. Hay que distinguir dos planos desde el principio porque tienen responsabilidades y tiempos distintos:

- **gsp / authoring time (offline, determinista):** resuelve refs → digests, vendoriza bytes, emite overlays *resueltos*. No aplica nada a un sistema vivo.
- **runtime / control plane (vivo):** el BEAM que controla los swarms consume overlays y los **actúa** sobre el árbol de supervisión OTP, asigna `seq`, gestiona drain/atomicidad/recovery.

El **overlay IR (IR2) es el contrato entre ambos planos.** `gsp` produce overlays resueltos; el runtime los actúa.

Piezas:

1. **El índice (`swarmidx`)** — un servicio que mapea referencias legibles a digests de contenido y los notariza. **No es un blob host.**
2. **El CLI (`gsp`)** — interfaz principal de authoring, diseñada para que la use un humano *o un agente* (Claude Code).
3. **El control plane** — el runtime Elixir/OTP que aplica overlays sobre swarms vivos (§4).

La web del índice existe solo para descubrir/navegar paquetes (análoga al sitio de crates.io frente a `cargo`).

### Principio rector del índice: notario, no almacén

npm/crates.io/PyPI hostean los bytes porque son anteriores al git hosting ubicuo. Go demostró que no hace falta: `proxy.golang.org` + `sum.golang.org` son un **resolver** (`(name, version) → digest`) y un **log de transparencia** que firma ese mapeo; los bytes viven en GitHub, content-addressed.

`swarmidx` sigue ese modelo. En esencia es una función:

```
swarmidx:jmlago/web-researcher@1.2.3  →  sha256:9f2c…  (source: github://jmlago/agents@v1.2.3, dir: packages/web-researcher)
```

más el log de transparencia. El registry entero cabe en un mapa name→digest + un Merkle log. No se operan petabytes. Minimiza superficie operativa y maximiza soberanía/reproducibilidad.

---

## 2. Las dos representaciones intermedias

### IR1 — `swarm.state`

Snapshot de un swarm: `agents`, `objects`, `topology`, `options`. Cada agente tiene slots tipados (`body`, `model`, `backend`); cada objeto tiene un `handler`. Las refs llevan `ref`, `digest` (cuando aplica) y `kind`.

**`swarm.state` es ambiguo sin un discriminador de fase** (ver §3): un mismo IR1 puede ser *desired* (lo que se quiere) u *observed* (lo que el runtime es ahora). Lleva `phase: desired | observed`.

### IR2 — `swarm.overlay`

Log de eventos ordenado sobre un `swarm.state`: `add_agent`, `add_topology_edges`, `scale_agent_group`, `bump_package`, `remove_agent`, … Dos roles simultáneos:

- **Control de versiones:** historia semántica, replayable, con blame y time-travel.
- **Control plane:** cada evento es un *comando a un sistema vivo* (§4).

**Decisión:** las mutaciones del CLI y del runtime se expresan como eventos de IR2. Nadie edita IR1 in-place.

---

## 3. Modelo de tres capas y ciclo de vida

Resuelve la pregunta "¿overlay-only o IR1 con info gsp?": **no es un XOR.** Hay tres capas.

1. **seed IR1** — autorado, inmutable, el origen. Puede llevar refs sueltas (`@^1.2`, sin digest).
2. **overlay log IR2** — append-only, la verdad del *cambio* y el control plane.
3. **IR1 materializado** = `fold(seed, overlays)` + resolve de digests. Es a la vez el estado **desired** y el **checkpoint** (recovery + compactación).

Así "IR1 lleva info gsp" es cierto, pero es el IR1 **materializado** (salida), no el **seed** (fuente). El seed queda limpio; las mutaciones viven en overlays; los digests aterrizan en el artefacto materializado.

### Desired vs Observed

En cuanto hay sistema vivo hay dos estados que divergen:

- **Desired** = `fold(seed, overlays)` + resolve. Lo que se quiere.
- **Observed** = lo que el OTP *es* ahora (agentes vivos, mailboxes, mensajes in-flight). Lo que pinta el panel ("agents 4 · supervised · t+00:04").

Ejemplo de divergencia: `scale coder → 3` es desired; si un `coder` crashea y reinicia, observed es 2 un instante. **El reconciler vive en el gap** y dirige observed → desired. Esto *es* la "supervised recovery": tras un crash, el sistema reconcilia contra el desired derivado del log + checkpoint.

### Autorado vs resuelto (sigue valiendo)

El seed autorado lleva constraints (`@^1.2`); `gsp resolve` lo compila a forma resuelta con digests inline. Hilemorfismo: el autorado es forma/potencia, el resuelto es el acto pinneado.

---

## 4. Mutación en vivo (control plane)

Los swarms mutan **mientras están activos**. Consecuencia directa: el in-place sobre IR1 está descartado — no se "edita el fichero de config" de un árbol de supervisión OTP que corre; se le mandan comandos. El overlay **es** el control plane (modelo Kubernetes: desired state + reconciler; en el stack: cambios dinámicos de child specs + hot code loading sobre supervisores).

### 4.1 Orden: single-writer por construcción

El overlay log lo escribe **siempre el mismo BEAM que controla el swarm**. Por tanto:

- El `seq` lo asigna ese proceso único → **orden total necesario, sin protocolo de consenso.** Es un log single-writer por arquitectura, no por convención.
- No hay carrera de `seq` posible: el emisor (agente, operador, autoscaler) *propone* una intención; el BEAM controlador es quien la *secuencia* y la materializa en el log.

> **Futuro (fuera de v1):** si en algún momento se quiere HA con controladores replicados, *ahí sí* el log necesitaría replicación/consenso. Hoy no.

### 4.2 Semántica de transición por-op

Lo que la mutación en caliente añade y el modelo offline ignoraba. Cada op que toca un sistema vivo lleva su política en el payload:

- **`remove_agent` / drain:** ¿qué pasa con el trabajo in-flight? Política `on_inflight: drain | kill | quarantine`.
  - `drain`: deja terminar el trabajo actual, no acepta nuevo, luego termina.
  - `kill`: descarta in-flight inmediatamente.
  - `quarantine`: para de aceptar, congela, decisión diferida.
- **`bump_package` / hot reload (requisito duro):** debe poder bumpear mid-flight live. Es hot code reload sobre un agente vivo. Política `migration: state_migrate | restart`.
  - `state_migrate`: preserva el estado acumulado a través del swap (estilo `code_change/3`).
  - `restart`: descarta estado, arranca limpio en la versión nueva.
  - Combina con `on_inflight` para el trabajo en curso de la versión vieja.
- **Atomicidad de overlay multi-evento:** un overlay con N eventos sobre un sistema vivo: `apply: incremental | transactional`.
  - `incremental`: se aplican uno a uno; cada op debe dejar el árbol de supervisión **válido entre medias**; hay estados intermedios observables.
  - `transactional`: stage → commit; el swarm no es observable en estado parcial.

### 4.3 Rollback → roll-forward

Offline, "rollback a seq N" = recomputar el fold. **Vivo no es reversible:** no se des-spawnea ni se des-envía un mensaje; el estado de un agente removido se fue. Los sistemas vivos hacen **roll-forward con eventos compensatorios**, no reversión literal.

- Time-travel para **inspección**: gratis (fold hasta seq N).
- Time-travel para **actuación**: emitir eventos compensatorios.

### 4.4 Checkpoint = IR1 materializado

El IR1 materializado no es solo un build artifact, es un **checkpoint** con sentido operativo:

- **Recovery:** restaurar un swarm desde el último checkpoint en vez de hacer replay del log entero.
- **Compactación:** snapshot del estado + truncar el log hasta ese `seq`. Resuelve el crecimiento ilimitado del log.

Es el patrón snapshot del event sourcing aplicado al runtime.

### 4.5 El puente supply-chain ↔ runtime

`bump_package` es donde se tocan los dos mundos:

1. `gsp` resuelve el digest nuevo (offline, contra `swarmidx` + transparency log) y emite el evento `bump_package` resuelto.
2. El runtime aplica el hot-swap con la política `migration`.

El **transparency log cubre el "qué bytes"**; el **runtime cubre el "cómo migra el estado"**. Juntos dan una historia de supply-chain end-to-end sobre la evolución viva del swarm.

---

## 5. Taxonomía de referencias

El eje "service ref vs package ref" es incorrecto (OCI es service-externo *pero* hasheable). Hay **dos ejes ortogonales**:

| ref | quién resuelve | content-addressable |
|---|---|---|
| `swarmidx:jmlago/coder@0.4.0` | swarmidx | sí (digest) |
| `oci:szc-agent-code` | externo (OCI) | sí (digest) |
| `openrouter:anthropic/claude-sonnet-4` | externo | no (`attested`) |
| `ssh` / `host` | externo | no |

`swarmidx` solo es responsable de la fila `swarmidx:`. El resto lo **pasa a través**: para OCI delega la verificación de digest al registro OCI; para refs no hasheables registra a lo sumo el flag `attested`.

---

## 6. Kinds de paquete

En el manifest (`swarmidx.json`), el `kind` es el **rol de slot**, no el tipo de bytes:

- `body` — persona/definición de agente (data)
- `policy` — IR de `llm-policy` (data)
- `handler` — código de objeto (code)
- `swarm` — un swarm IR completo y resuelto, publicado como unidad componible

El resolver usa el `kind` para **typing de slots**: rechaza meter un `kind: handler` en un slot `model`, etc.

> **Abierto:** `data`/`code` parece *derivable* del kind (body/policy → data, handler → code). Confirmar que no hay un kind que sea a veces uno y a veces otro; si no, no es campo del manifest.

### 6.1 Qué debería ser un package (y qué no)

El criterio se deriva de "kind = rol de slot" y conviene hacerlo explícito:

> **Un package es lo que un swarm referencia por contenido para constituirse: lo que
> rellena un slot del IR.** La prueba operativa: *¿pueden `gsp add` o
> `gsp materialize --resolve` hacer algo con él?* Si la respuesta es no, no es un
> package — es otra cosa con otro cauce.

Pasan el criterio: bodies (qué es el agente), policies (cómo elige modelo), handlers
(qué objeto corre), swarms (la composición entera). Quedan fuera **tres clases**, cada
una con su cauce propio:

1. **Sustrato** — el engine genswarms, subzeroclaw, el router LLM. El swarm corre
   *sobre* ellos; ningún slot los referencia. Su cauce: releases/instalación propios.
   Empaquetarlos sería catalogar la infraestructura, no componer el swarm.
2. **Clientes y observadores externos** — UIs, frontends, CLIs, cualquier cosa que
   mira el swarm desde fuera por un API. No tienen slot, la topología no los gatea,
   el resolver no tiene nada que resolver. Su cauce: artefactos de deploy (imagen,
   repo), enlazados desde la ficha del package si acompañan a uno (campos
   `docs`/`skill` del §7).
3. **Código del host por definición** — implementaciones de behaviours que un package
   deja deliberadamente al host (un `DataSource`, un adapter app-specific). Es
   app-specific por diseño: no hay bytes generales que notarizar. Su cauce: el repo
   del host. (El adapter *de referencia* entre dos packages vive en el package que
   posee los específicos — p. ej. el DataSource de referencia para un transporte
   Telegram pertenece al package de Telegram, no al host.)

**Objetivar: convertir una capacidad en package.** El camino para algo que hoy es una
librería/boot-script y *debería* ser slot content es darle forma de objeto primero:

1. Envolver el lifecycle en un **object handler** (`init/1` arranca con config
   puro-dato, `terminate/2` para determinista, `handle_message/3` con un protocolo
   JSON mínimo — al menos `{"action":"status"}` —, `interface/0` lo documenta).
2. Config = **datos, no código**: refs a módulos como átomos (defs Elixir) o strings
   (IR JSON) resueltos con `to_existing_atom` (sin mintear átomos; módulo desconocido
   ⇒ init falla, fail-closed). Defaults utilizables sin código del host cuando sea
   posible (un `Null` data source vale más que un required).
3. Sin dep de compilación sobre el engine: callbacks **por convención**, no
   `@behaviour` — genswarms es peer/runtime dep y la librería compila sola.
4. `swarmidx.json` con `kind: handler` y **`dir` apuntando solo a lo que el swarm
   carga** (si el repo trae además un frontend/cliente, fuera del `dir`: el digest
   debe cubrir exactamente lo consumible).
5. Publicar (tag). Referencias: `genswarms-telegram` (Ingress/Sender) y
   `genswarms-dashboard` (`Objects.Dashboard` + `DataSource.Null`).

Añadir un `kind` nuevo para las clases excluidas (p. ej. `app`/`ui` para frontends) se
rechazó deliberadamente: un kind sin semántica de resolución convierte el registry en
un catálogo de links — otra finalidad. Si algún día existe una historia real de
attachments operacionales por swarm, se diseña entonces con su semántica.

---

## 7. El manifest: `swarmidx.json`

JSON en todo el sistema — un solo formato, un solo parser, un solo modelo mental (los IR ya son JSON; el manifest también). Donde harían falta comentarios, se usa un campo `description`/`note`. En la raíz del repo:

```json
{
  "registry": { "scope": "jmlago" },
  "packages": [
    { "name": "web-researcher", "dir": "packages/web-researcher", "kind": "body",
      "description": "Web research agent body" },
    { "name": "cost-router", "dir": "packages/cost-router", "kind": "policy",
      "description": "Cost-aware LLM routing policy" },
    { "name": "task-board", "dir": "packages/task-board", "kind": "handler" },

    { "name": "strict-reviewer", "dir": "packages/strict-reviewer", "kind": "body",
      "note": "deps explícitas SOLO cuando no son inferibles del contenido (ver §9)",
      "deps": ["jmlago/cost-router@^1.0"] }
  ]
}
```

- `scope` **debe** coincidir con el owner de GitHub; se verifica al publicar.
- **Sin campo `version` por paquete** — la versión la aporta el tag (§8).
- El `swarmidx.json` no se hashea: lo dirasheado es el contenido de cada `dir`, no el manifest.
- Metadata de ficha (opcional, no notarizada): `description`, y `docs` / `skill` —
  ruta relativa a la raíz del source (p.ej. `docs/guide.md`) o URL absoluta. Si el
  manifest no los declara, el notary detecta `README.md` / `SKILL.md` en el `dir`
  del paquete (en el mismo clone en que hashea) y los usa como fallback.

---

## 8. Versionado y publicación

### El tag es la versión

`version` en el manifest *y* tag = dos fuentes de verdad que divergen. **El manifest describe estructura; el tag aporta versión.** Una fuente para cada cosa (lección de Go modules).

Publicar:

```
git tag v1.2.3 && git push --tags
```

El registry lee `swarmidx.json` en ese commit, computa el digest de cada `dir`, y registra `(jmlago/<name>, 1.2.3) → digest, source`. Vía webhook (estilo deno.land/x) no hace falta un paso `gsp publish` separado, aunque puede ofrecerse como atajo. La identidad de GitHub da el namespace gratis (`jmlago/` ligado al handle) y mata el squatting.

### Monorepo: lockstep ahora, per-paquete después

- **Lockstep (recomendado para empezar):** un repo = una línea de versión; un tag versiona todos los paquetes del manifest.
- **Per-paquete (futuro):** tags por subdirectorio estilo Go (`coder/v0.4.0`). Migrar solo si la cadencia de un paquete diverge de verdad.

---

## 9. Dependencias

### Regla: declarar solo lo no inferible del contenido

- **`kind: swarm`** → **no necesita sección `deps`.** Sus deps *son* los refs que ya viven en su IR. El resolver los camina.
- **`kind: body` / `policy` / `handler`** con deps no descubribles desde el slot (un body con policy fija, un handler que importa otro handler) → se declaran en `deps`. Objeto→objeto (`task-board` depende de `kv-store`) es este caso.

### Dos grafos distintos — no confundir

- **Grafo de dependencias** (quién-necesito-para-resolver): **DAG obligatorio.** Un `kind: swarm` que referencia a otro `kind: swarm` es arista de este grafo → ciclos prohibidos.
- **Topología de mensajes** (quién-le-habla-a-quién): **puede tener ciclos** (`researcher ↔ task_board`). Independiente del grafo de deps.

### Composición Merkle

Publicar un `kind: swarm` = publicar su IR resuelto. Su entrada en el transparency log **commitea transitivamente a los digests de todas sus deps**. Si cambia cualquier dep, cambia el digest del swarm.

---

## 10. Digest reproducible (`dirhash`)

El digest sobre un `dir` tiene que ser recomputable por cualquier cliente. **No** hashear el git tree ni un tar (sensibles a orden/mtime/permisos). Dirhash estilo Go: lista ordenada de `sha256(bytes)  path` por fichero dentro de `dir`, y hashear esa lista. Determinista, independiente del empaquetado git.

---

## 11. El CLI (`gsp`) — authoring, agent-facing

`gsp` opera en el plano offline: resuelve y emite overlays *resueltos*; no actúa sobre sistemas vivos (eso es el control plane, §4).

| Comando | Qué hace |
|---|---|
| `gsp resolve` / `gsp sync <ir>` | Determinista. Resuelve refs, rellena digests, vendoriza bytes. `cargo build` / `go mod download`. |
| `gsp add <pkg> --as <slot:name> [--wire <obj>] [--on-inflight drain] [--migration restart]` | Emite un evento `swarm.overlay` (`add_agent`, …) ya con su política de transición. |
| `gsp bump <agent> --field body [--migration state_migrate]` | Emite un `bump_package` resuelto (nuevo digest). El runtime hace el hot-swap. |
| `gsp materialize <seed> <overlays...>` | `fold` + resolve → IR1 materializado (checkpoint / desired). |
| `gsp verify <ir>` | Recomputa digests y verifica contra el transparency log. |
| `gsp publish` | Atajo opcional sobre el flujo `git tag`. |

**Requisitos agent-facing (Claude Code):** salida `--json` estable, idempotencia, exit codes semánticos. Materializa la promesa del landing ("hand it to your agent").

---

## 12. Log de transparencia

Dado el flag `"attested": true` y la orientación a atestación/soberanía, el transparency log (Merkle append-only + firma, estilo `sumdb`) **no es opcional**: impide que un digest se cambie en silencio. Compone con `attested` y con `bump_package` (§4.5) para una historia de verificación end-to-end del IR, incluyendo su evolución viva.

---

## 13. Preguntas abiertas

1. ~~`gsp add`: overlay-only vs in-place~~ → **Resuelto (§3, §4):** overlay es el control plane; tres capas; in-place descartado.
2. **`data`/`code`: ¿derivable del `kind` o campo explícito?** (§6)
3. **Defaults de las políticas de transición** (`on_inflight`, `migration`, `apply`): ¿cuáles son seguros por defecto? Probablemente `drain` + `restart` + `incremental`.
4. **Cadencia de checkpoint/compactación:** ¿cada N eventos, por tiempo, o explícito? (§4.4)
5. **¿Sección `deps` necesaria el día uno** o se difiere? (§9)
6. **Transparency log: ¿v1 o iteración posterior?**
7. **Caché/vendoring local:** dónde se materializan los bytes resueltos y cómo se garbage-collectan.

---

## 14. Dirección futura (fuera de ámbito v1)

- **HA del control plane:** controladores replicados → el overlay log necesitaría replicación/consenso (§4.1). Hoy single-writer por BEAM.
- **Phylogenesis:** `seed` (IR1) + overlays (IR2) ≅ genoma + mutaciones; los overlays como diff genético sobre el seed, con `gsp` como capa de distribución/versionado del material genético.
