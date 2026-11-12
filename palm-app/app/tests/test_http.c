#include "check.h"
#include "../http.h"

int main(void)
{
	Char req[512], form[96], tiny[20];
	UInt16 n, status, off, len;
	const char *reply = "HTTP/1.1 401 Unauthorized\r\nContent-Type: text/plain; charset=windows-1252\r\n"
		"Connection: close\r\n\r\nerror=login_required\r\n";

	n = httpRequest(req, sizeof req, "GET", "10.42.0.1", "/api/v1/status?format=text", "abc123", NULL);
	check("GET", n > 0 && strcmp(req,
		"GET /api/v1/status?format=text HTTP/1.1\r\nHost: 10.42.0.1\r\nUser-Agent: palm-gw-palm/1\r\n"
		"Connection: close\r\nAuthorization: Bearer abc123\r\n\r\n") == 0);

	form[0] = 0;
	check("form 1", httpFormAdd(form, sizeof form, "username", "sysadmin"));
	check("form 2", httpFormAdd(form, sizeof form, "password", "a b&c=d\xe9"));
	check("form encoded", strcmp(form, "username=sysadmin&password=a%20b%26c%3Dd%E9") == 0);
	n = httpRequest(req, sizeof req, "POST", "10.42.0.1", "/api/v1/login?format=text", NULL, form);
	check("POST", n > 0 && strstr(req, "Content-Type: application/x-www-form-urlencoded\r\nContent-Length: 43\r\n\r\nusername=") != NULL &&
		strstr(req, "Authorization") == NULL);
	check("too small", httpRequest(tiny, sizeof tiny, "GET", "10.42.0.1", "/", NULL, NULL) == 0);
	tiny[0] = 0;
	check("form too small", !httpFormAdd(tiny, sizeof tiny, "password", "0123456789012"));

	check("reply", httpReply(reply, (UInt16)strlen(reply), &status, &off, &len));
	check("status 401", status == 401);
	check("body", len == 22 && strncmp(reply + off, "error=login_required\r\n", len) == 0);
	check("no header end", !httpReply(reply, 40, &status, &off, &len));
	check("not HTTP", !httpReply("SSH-2.0-OpenSSH_9.9\r\n\r\n", 23, &status, &off, &len));
	return failures != 0;
}
