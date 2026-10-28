# WatchZit appliance: the setup menu starts when wzadmin signs in on the
# server's own screen (not over SSH). Leave it for a command line; open it
# again with: sudo watchzit-setup
case "$(tty 2>/dev/null)" in
/dev/tty[0-9]*)
    if [ "$(id -un)" = wzadmin ] && [ -z "${WZ_MENU_SHOWN:-}" ]; then
        export WZ_MENU_SHOWN=1
        sudo -n /usr/local/sbin/watchzit-setup
    fi
    ;;
esac
