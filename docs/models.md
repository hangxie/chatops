# Models

How well each model we tried drives chatops, measured with [`scripts/model-check.sh`](../scripts/model-check.sh). Use it to pick a model and to see what a weak one gets wrong.

## How they were tested

- Date: 2026-10-18, over several rounds.
- Cases: the 29 in [`scripts/model-check.cases`](../scripts/model-check.cases), run against a homelab with the `ping`, `host`, `status`, and `kubernetes` tool groups. nemotron-3.5-super ran an earlier set of 27, without `svc-claude-code` and `svc-codex`.
- Rounds: the rows marked *current* ran after the tool descriptions were sharpened and `service_status` took a list. The others ran before, so they may do better now.
- Settings: those in [Recommended settings](#recommended-settings), with `agent.show_stats: true` and `agent.turn_timeout` raised past `120s`. The script waited at most 300 s for each reply.
- The raw result files are not kept here: their replies carry public addresses and host details.

## Recommended settings

The provider decides which request fields are accepted; see [Model providers](../README.md#model-providers) for why each one is set.

| Served by | `disable_thinking` | `stream` | `agent.require_tool_call` | `/no_think` in `system_prompt` | `agent.turn_timeout` |
|---|---|---|---|---|---|
| Cloudflare Workers AI | `false`: `true` fails every turn with code `8001` | `true` | `true` | no | `120s` |
| llama.cpp, Qwen3 family | `true` | `true` | `true` | yes | `120s` for 4B; `300s` for 27B |
| vLLM behind LiteLLM | `true` | `true`, so a timeout stops generation | depends on the model: `false` for Qwen3 (see below) | Qwen3 family only | `120s` |

- Keep thinking off for small models (3–4B) on llama.cpp and vLLM.
- None of the tested models needed `stream: false`. Turn it off only for a provider that rejects `stream_options` or mangles streamed tool calls. That shows up as HTTP 400, or broken tool arguments, on every turn.
- On vLLM, run without `--enforce-eager`. With it, qwen-3.5-4b wrote about 8–11 tokens/s on an RTX 3050 (6 GB); without it, about 31.

On vLLM, set `require_tool_call` per model, not per server. vLLM forces `required` as JSON through guided decoding:

| Model | Tool-call format | With `require_tool_call: true` |
|---|---|---|
| qwen-3.5-4b | XML, which the `qwen3_coder` parser expects | the first call ran away: a hung request generated about 2,600 tokens in 300 s |
| llama-3.2-3b | JSON, the format guided decoding forces | ran normally |

With `false`, every qwen-3.5-4b case answered, and it still sent greetings and jokes to the decline tool on its own.
- Raise `agent.turn_timeout` for a server below about 20 tokens/s: a long list takes over 120 s to write there (see [Lessons](#lessons)).

## Retry codes per provider

These lists come from each provider's error documentation, read in October 2026. Providers change their error codes over time:

- Check the linked page before relying on a list.
- After a change, watch `chatops_model_failovers_total` and the `model failed over` warnings.

The default `[429, 5xx]` covers outages and rate limits everywhere. Each provider also has statuses that mean "this endpoint or account cannot serve you right now", such as a revoked key or used-up credit. The backup is not affected by them:

| Provider | Recommended `retry_http_codes` | Added to the default, and why |
|---|---|---|
| llama.cpp, Ollama | `[401, 404, 429, 5xx]` | 401: wrong `--api-key`. 404: Ollama has not pulled the model. 503 is already covered: llama.cpp returns it while the model loads, Ollama when its queue is full |
| vLLM | `[401, 404, 408, 429, 5xx]` | 401: wrong key. 404: `model` is not a name the server serves. 408: LiteLLM's status for an upstream timeout |
| OpenRouter | `[401, 402, 408, 429, 5xx]` | 401: invalid key. 402: out of credits. 408: request timed out. 502 and 503 (model down, no provider available) are already covered |
| Cloudflare Workers AI | `[401, 403, 408, 429, 5xx]` | 401, 403: bad token, or a model the account or plan cannot use. 408: timed out or aborted. 429 also means the free plan's daily allocation is used up |
| Gemini | `[401, 402, 403, 429, 5xx]` | 401: missing, invalid, or expired key. 402: prepaid credit used up. 403: key lacks access. 429 also means the daily quota is used up |
| OpenAI | `[401, 403, 429, 5xx]` | 401: revoked key or IP not allowed. 403: unsupported region. 429 also means no credit left or a spend limit reached |
| Anthropic | `[401, 402, 403, 429, 5xx]` | 401: invalid or expired key. 402: billing problem. 403: key lacks permission. 529 (overloaded) is already covered by `5xx` |

Never add these:

- 400 on any provider. It mostly means the request itself is wrong, such as a context that is too long, and the backup would reject it too.
    - Two account problems are 400s, so they do not fail over: Anthropic's spend limits you set yourself, and Gemini's disabled billing (`failed_precondition`).
- 403 on OpenRouter. Besides permissions, it means a moderation flag on the message, which should not be sent on to another model.
- 413 and 422. They describe the request, not the endpoint.

OpenRouter and Anthropic can send an error inside a stream, after HTTP 200. It has no status to match, so it does not fail over.

Sources:

- [OpenRouter](https://openrouter.ai/docs/api-reference/errors)
- [Cloudflare Workers AI](https://developers.cloudflare.com/workers-ai/platform/errors/)
- [Gemini](https://ai.google.dev/gemini-api/docs/api-errors)
- [OpenAI](https://developers.openai.com/api/docs/guides/error-codes)
- [Anthropic](https://platform.claude.com/docs/en/api/errors)
- [LiteLLM](https://docs.litellm.ai/docs/exception_mapping)

## Hardware

A single 6 GB GPU, such as an RTX 3050, is not enough to run chatops well.

- The models tested there were up to about 4B parameters, at 4 bits, with a 16k context.
- A larger model would need heavier quantization, a shorter context, or CPU offload. None of these were tried.
- Of the models tested there, only qwen-3.5-4b (AWQ 4-bit) gave acceptable results.

Tested on vLLM with one RTX 3050 (6 GB):

| Model | Pass | Fail | Manual | Speed | Result |
|---|---|---|---|---|---|
| qwen-3.5-4b (AWQ 4-bit) | 25 | 2 | 2 | ~31 tokens/s | Acceptable |
| llama-3.2-3b | 20 | 7 | 2 | ~35 tokens/s | Not suitable |
| granite-4.1-3b | 18 | 9 | 2 | - | Not suitable |

- qwen-3.5-4b still checks only the host for "the media server and everything on it" and calls an unlisted host unreachable instead of not covered.
- The models that score best (26 of 29) are 26–27B.

qwen-3.5-4b scored about the same on both servers, but it is easier to run on llama.cpp:

| Server | Settings | Speed |
|---|---|---|
| llama.cpp on a Mac | defaults | about 45 tokens/s |
| vLLM on an RTX 3050 | `agent.require_tool_call: false` and no `--enforce-eager`, or it hangs | about 31 tokens/s; a long answer such as a pod list takes over a minute |

## Results

Two cases have no automatic checks and are left for a human: `capabilities` and `svc-uncovered`.

| Model | Served by | Round | Pass | Fail | Manual | Verdict |
|---|---|---|---|---|---|---|
| qwen-3.8-27b | llama.cpp on a Mac | earlier | 26 | 1 | 2 | Accurate but slow |
| gemma-4-26b | Cloudflare Workers AI | earlier | 26 | 1 | 2 | Good |
| glm-4.7-flash | Cloudflare Workers AI | earlier | 25 | 2 | 2 | Good |
| qwen-3.5-4b (AWQ 4-bit) | vLLM on an RTX 3050 (6 GB) | current | 25 | 2 | 2 | Good for its size |
| qwen-3.5-4b (Q4 GGUF) | llama.cpp on a Mac | current | 24 | 3 | 2 | Good for its size |
| nemotron-3.5-super | Cloudflare Workers AI | earlier | 22 of 27 | 3 | 2 | Declines a plain `ping` |
| llama-3.2-3b | vLLM on an RTX 3050 (6 GB) | current | 20 | 7 | 2 | Not suitable: invents a tool and results |
| granite-4.1-3b | vLLM on an RTX 3050 (6 GB) | current | 18 | 9 | 2 | Not suitable: invents tool names and results |

## What went wrong

**qwen-3.8-27b**

- Gave no reply to "is nas.xiehang.com reachable?". Its first model call, which must call a tool, ran the whole 5-minute turn.
- Before host results showed addresses, it answered that question by checking the `home` host.
- `pods-ns` finished in 139 s, at about 17 tokens/s.

**gemma-4-26b**

- `node-usage` failed after `node_list`: chatops rejected a streamed tool call with index 16.
- chatops now accepts indices that are not contiguous.

**glm-4.7-flash**

- For "the media server and everything on it", it checked the media LLM but not Harbor. `service_status` took one service per call then.
- It sent "is nas.xiehang.com reachable?" to the decline tool.

**qwen-3.5-4b, both servers**

- For "the media server and everything on it", it checked only the host.
- It called `nas.xiehang.com` "not reachable" instead of not covered.
- On llama.cpp, it also sent "ping and tell me a joke" to the decline tool.
- Before the description changes, it sent "the chat bot" and "llama.cpp on media" to `host_status`. Now both go to `service_status`.

**nemotron-3.5-super**

- Sent a plain `ping`, "ping and tell me a joke", and "is nas.xiehang.com reachable?" to the decline tool.

**granite-4.1-3b**

- Called tools that do not exist: `host__host_localhost`, `kubernetes__pod_healthy`.
- Said `nas.xiehang.com` failed to resolve, when no tool reported that.
- Answered "tell me a joke" without the decline tool.
- Answered "are all my hosts up?" without naming any host.
- Checked hosts for Kubernetes nodes.
- Failed `pods-ns` after almost 5 minutes. In an earlier round, it hit the 4,096-token cap there.

**llama-3.2-3b**

- Called `kubernetes__service_status`, which does not exist, while looking for Harbor in Kubernetes.
- Answered "is nas.xiehang.com reachable?" with "The NAS at media.xiehang.com is reachable".
- Said there are no `worker-*` nodes, right after `node_list` listed them.
- After a plain `ping`, it kept calling `host_status` until the repeat guard stopped it.
- Called `namespace_list` five times for one question.
- In an earlier round, it passed `worker-*` as a node name, and retried with malformed arguments.

## Not suitable

Models ruled out, with what disqualified them. Add a row when a model fails for a reason no setting fixes.

| Model | Served by | Disqualified for |
|---|---|---|
| llama-3.2-3b | vLLM | Called a tool that does not exist, mapped `nas.xiehang.com` onto the `media` host, and denied nodes that `node_list` had just returned. |
| granite-4.1-3b | vLLM | Called tools that do not exist and reported results no tool returned, with `require_tool_call` on and off. |

Any of these rules a model out, whatever its score:

- It calls a tool that is not in the list.
- It reports a status that no tool returned.
- It maps an unlisted host or service onto a listed one, now that tool choices show each host's address.
- It answers an off-topic message as if it were in scope, with `require_tool_call: false`.

## Lessons

- Small models (3–4B) struggle to pick between `host_status` and `service_status`.
    - Host `covers` that name services ("the Harbor and LLM host") pull service questions onto the host.
    - So keep host `covers` to names for the machine itself.
- Tool descriptions steer routing as much as `covers` do.
    - With clean `covers`, qwen-3.5-4b still sent "the chat bot" to `host_status`, whose description named the ChatOps server.
    - The description now says it checks the machine only, not what runs on it. qwen-3.5-4b routes both correctly.
- Models stop after one call when each target needs its own. `service_status` now takes a list, like `host_status` and the Kubernetes tools.
- Every model sent "what can you do?" to the decline tool.
- A long list is limited by output speed, not by the tool. `pod_list` for `kube-system` returns 6.7 KB in under 100 ms. Every model then rewrote it as a table of about 2,500 tokens:

| Model | Speed | Time to write the table |
|---|---|---|
| glm-4.7-flash | 115 tokens/s | about 20 s |
| qwen-3.5-4b on llama.cpp | 45 tokens/s | about 55 s |
| qwen-3.5-4b on vLLM | 31 tokens/s | about 79 s |
| qwen-3.8-27b | 17 tokens/s | about 139 s |
