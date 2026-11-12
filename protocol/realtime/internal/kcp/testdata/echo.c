/* Reference KCP peer for the Go interop test: echoes every KCP message it
 * receives over UDP on 127.0.0.1, using the upstream ikcp.c. Prints the
 * bound port, then runs until killed. Usage: echo <conv> */
#include <arpa/inet.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <unistd.h>
#include "ikcp.h"

static int sock;
static struct sockaddr_in peer;
static int have_peer;

static IUINT32 now_ms(void) {
    struct timeval tv;
    gettimeofday(&tv, NULL);
    return (IUINT32)(tv.tv_sec * 1000 + tv.tv_usec / 1000);
}

static int output(const char *buf, int len, ikcpcb *kcp, void *user) {
    (void)kcp; (void)user;
    if (have_peer)
        sendto(sock, buf, len, 0, (struct sockaddr *)&peer, sizeof(peer));
    return 0;
}

int main(int argc, char **argv) {
    if (argc != 2) return 2;
    IUINT32 conv = (IUINT32)strtoul(argv[1], NULL, 10);
    sock = socket(AF_INET, SOCK_DGRAM, 0);
    struct sockaddr_in addr = {0};
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    if (bind(sock, (struct sockaddr *)&addr, sizeof(addr)) != 0) return 3;
    socklen_t n = sizeof(addr);
    getsockname(sock, (struct sockaddr *)&addr, &n);
    printf("%d\n", ntohs(addr.sin_port));
    fflush(stdout);

    ikcpcb *kcp = ikcp_create(conv, NULL);
    kcp->output = output;
    ikcp_nodelay(kcp, 1, 10, 2, 1);
    ikcp_wndsize(kcp, 256, 256);
    ikcp_setmtu(kcp, 1200);
    kcp->dead_link = 1u << 30;
    static char buf[1 << 20];
    for (;;) {
        fd_set fds;
        FD_ZERO(&fds);
        FD_SET(sock, &fds);
        struct timeval tv = {0, 1000};
        if (select(sock + 1, &fds, NULL, NULL, &tv) > 0) {
            socklen_t len = sizeof(peer);
            int got = recvfrom(sock, buf, sizeof(buf), 0, (struct sockaddr *)&peer, &len);
            if (got > 0) {
                have_peer = 1;
                if (ikcp_input(kcp, buf, got) < 0) return 4;
            }
        }
        int size;
        while ((size = ikcp_recv(kcp, buf, sizeof(buf))) >= 0)
            if (ikcp_send(kcp, buf, size) < 0) return 5;
        ikcp_update(kcp, now_ms());
    }
}
