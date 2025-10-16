vcpkg_from_github(
    OUT_SOURCE_PATH SOURCE_PATH
    REPO avahi/avahi
    REF "v${VERSION}"
    SHA512 27bba9a551152dfc7e721f326042e7bfce55d227044a6cbaee04d6fb0e3f59c36e159c2b7a4dd42d1c955cdf37cc1c303e91991c08928bbded91d796e9a22abe
    HEAD_REF master
)

vcpkg_configure_make(
    SOURCE_PATH "${SOURCE_PATH}"
    AUTOCONFIG
    COPY_SOURCE
    OPTIONS
        --with-distro=none
        --disable-python
        --disable-manpages
        --disable-libevent
        --disable-gtk
        --disable-gtk3
        --disable-mono
        --disable-monodoc
        --disable-qt4
        --disable-qt5
        --disable-glib
        --disable-gobject
        --disable-libdaemon
        --disable-gdbm
)

vcpkg_install_make()
vcpkg_fixup_pkgconfig()

vcpkg_install_copyright(FILE_LIST "${SOURCE_PATH}/LICENSE")
