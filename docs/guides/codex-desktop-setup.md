# Set up Codex in a bot computer with a ChatGPT plan

## Before you start

Use a bot.Box deployment with Codex desktop support, a ChatGPT account eligible for plan usage in the integration, and permission to manage the bot and take control of its computer. Use your own workstation terminal for the connection step and the bot computer terminal for Codex. [ChatGPT plan eligibility](https://developers.openai.com/siwc/token-sharing-open-source)

### Update a local development deployment

From a `devenv` shell at the root of an updated bot.Box checkout, build the sandbox and egress proxy images. Run the updated API and frontend using the [development setup](../../README.md). In the bot workspace, use **Restart** to provision the updated bot computer.

```sh
make sandbox-image proxy-image
```

## Connect your ChatGPT account

1. Open the target bot and select **Secrets & network → ChatGPT plan for Codex → Continue with ChatGPT**. Keep the page open and copy the one-time pairing code.
2. On your **workstation**, open the updated bot.Box checkout in a `devenv` shell and run the following command. Select the bot.Box URL shown on the connection page and enter the pairing code when prompted.

   ```sh
   make codex-login
   ```

3. In the browser opened by the helper, select your ChatGPT account, choose the workspace if prompted, and approve use of your plan. If the helper prints a sign-in link, open it in the browser on the same workstation. Return to the bot page and confirm that the card shows **Connected**. bot.Box stores the authorization outside the bot computer.

## Start Codex

1. As the connected bot.Box user, open the bot computer and select **Take control**. Resume the bot first if it is paused.
2. Open a terminal in the **bot computer**, change to the directory you want to work in, and start Codex.

   ```sh
   codex
   ```

3. Submit a short prompt to confirm the connection. Codex uses the bot computer's files and terminal. Open the bot's **Secrets & network → Bot budget limits** section to see input, output, and cached-input token totals after the request completes. Choose **Hand back** when finished.

## Apply an internal budget charge

1. As a workspace owner or admin, open **Workspace settings → Budgets & model prices**.
2. Save an **OpenAI** price for the exact model ID selected in Codex. After the first completed request, you can also find the effective model ID in the bot's usage view. Rates are in USD per million tokens. Enter the rates for uncached input, output, and cached input.
3. Enable **Count subscription usage toward budgets** and set the bot or workspace spending cap. New eligible requests show an **Internal budget charge**. ChatGPT manages the plan allowance and any provider billing independently.

A Codex request already in progress can cross the bot.Box cap. Once that request has been accounted for, bot.Box blocks the next call if the budget is exhausted. With the option off, the token totals remain available. [OpenAI preview limits](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations)

## Resolve common issues

| What you see                                          | What to check                                                                                                                                                                                                |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Codex asks you to sign in inside the bot computer     | Check that the computer is running the updated image and the bot's connection shows **Connected**. Complete any account prompts in the workstation helper.                                                   |
| Codex says it cannot use the connection               | Sign in to bot.Box as the user who connected the ChatGPT account, resume the bot if paused, and take desktop control. Use **Reconnect** if the session has expired or was revoked.                           |
| The ChatGPT plan or model is unavailable              | Confirm that the connected account and workspace permit plan usage and select an eligible model in Codex. Check [ChatGPT usage settings](https://chatgpt.com/#settings/usage) for the plan or app allowance. |
| Bot or workspace budget reached                       | Check **Bot budget limits** in **Secrets & network** and the workspace budget. Ask a workspace admin to review the applicable limit or an unresolved usage entry.                                            |
| Codex reports a missing model price                   | When internal charging and a monetary cap apply, a workspace admin must add a price for the exact model ID before the request can start.                                                                     |
| Tokens are visible but the internal charge is missing | Check that the workspace setting is enabled and that the price matches the model ID. Policy and price changes apply to new requests.                                                                         |

To remove bot.Box access, use **Disconnect** in the bot's connection card. You can also review the app connection in ChatGPT settings. [OpenAI account and session management](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions)
