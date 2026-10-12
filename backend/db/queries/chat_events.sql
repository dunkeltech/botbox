-- name: GetBotConversation :one
SELECT * FROM conversations WHERE workspace_id=$1 AND bot_id=$2 AND id=$3;
